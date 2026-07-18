// Package memory is Kibborg's long-term memory: a SQLite-backed store of verbatim
// conversation episodes (each with an optional embedding) plus a rolling per-chat summary.
// It is the "правильная память" layer that lets the agent recall things older than the small
// working window without inventing them — recall returns real past turns, not paraphrases,
// which is exactly what keeps the agent from hallucinating remembered facts.
//
// The driver is modernc.org/sqlite (pure Go, no cgo), so it builds in this cgo-less setup.
package memory

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Episode is one stored user↔assistant exchange.
type Episode struct {
	ID        int64
	ChatID    int64
	User      string
	Assistant string
	TS        int64   // unix seconds
	Score     float64 // similarity/relevance, set by Recall (not persisted)
}

// Store wraps the SQLite database. All methods are safe for concurrent use.
type Store struct {
	db *sql.DB
	mu sync.Mutex // serializes writes; modernc handles concurrency but this keeps it simple
}

// Open opens (creating if needed) the memory database at path and ensures the schema.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer avoids "database is locked" churn for our tiny load
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS episodes (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id        INTEGER NOT NULL,
  user_text      TEXT NOT NULL,
  assistant_text TEXT NOT NULL,
  embedding      BLOB,
  ts             INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_episodes_chat ON episodes(chat_id, id);

CREATE TABLE IF NOT EXISTS summaries (
  chat_id    INTEGER PRIMARY KEY,
  text       TEXT NOT NULL,
  updated_ts INTEGER NOT NULL
);`
	_, err := s.db.Exec(schema)
	return err
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// AddEpisode stores one exchange. emb may be nil (no embedding model configured); recall then
// falls back to keyword matching for this row.
func (s *Store) AddEpisode(chatID int64, user, assistant string, emb []float32) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO episodes(chat_id, user_text, assistant_text, embedding, ts) VALUES(?,?,?,?,?)`,
		chatID, user, assistant, encodeVec(emb), time.Now().Unix())
	return err
}

// Recall returns up to limit past episodes for the chat most relevant to the query. If q is a
// non-empty embedding it ranks by cosine similarity; otherwise it ranks by keyword overlap
// with queryText. Results are ordered most-relevant first.
func (s *Store) Recall(chatID int64, q []float32, queryText string, limit int) ([]Episode, error) {
	if s == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 4
	}
	if len(q) > 0 {
		return s.recallVector(chatID, q, limit)
	}
	return s.recallKeyword(chatID, queryText, limit)
}

// scanLimit bounds how many recent rows we pull into memory for scoring, keeping recall O(1)
// in DB size for a busy chat.
const scanLimit = 2000

func (s *Store) recallVector(chatID int64, q []float32, limit int) ([]Episode, error) {
	rows, err := s.db.Query(
		`SELECT id, user_text, assistant_text, embedding, ts FROM episodes
		 WHERE chat_id=? AND embedding IS NOT NULL ORDER BY id DESC LIMIT ?`,
		chatID, scanLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var eps []Episode
	for rows.Next() {
		var e Episode
		var blob []byte
		if err := rows.Scan(&e.ID, &e.User, &e.Assistant, &blob, &e.TS); err != nil {
			return nil, err
		}
		e.ChatID = chatID
		e.Score = cosine(q, decodeVec(blob))
		eps = append(eps, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Keep only meaningfully-similar hits; a low floor avoids injecting noise into the prompt.
	const minSim = 0.30
	filtered := eps[:0]
	for _, e := range eps {
		if e.Score >= minSim {
			filtered = append(filtered, e)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Score > filtered[j].Score })
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *Store) recallKeyword(chatID int64, queryText string, limit int) ([]Episode, error) {
	tokens := tokenize(queryText)
	if len(tokens) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, user_text, assistant_text, ts FROM episodes
		 WHERE chat_id=? ORDER BY id DESC LIMIT ?`,
		chatID, scanLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var eps []Episode
	for rows.Next() {
		var e Episode
		if err := rows.Scan(&e.ID, &e.User, &e.Assistant, &e.TS); err != nil {
			return nil, err
		}
		e.ChatID = chatID
		hay := strings.ToLower(e.User + " " + e.Assistant)
		hits := 0
		for _, t := range tokens {
			if strings.Contains(hay, t) {
				hits++
			}
		}
		if hits > 0 {
			e.Score = float64(hits)
			eps = append(eps, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Rank by token overlap, then recency (higher id first) as a tie-break.
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].Score != eps[j].Score {
			return eps[i].Score > eps[j].Score
		}
		return eps[i].ID > eps[j].ID
	})
	if len(eps) > limit {
		eps = eps[:limit]
	}
	return eps, nil
}

// GetSummary returns the rolling summary for a chat ("" if none).
func (s *Store) GetSummary(chatID int64) (string, error) {
	if s == nil {
		return "", nil
	}
	var text string
	err := s.db.QueryRow(`SELECT text FROM summaries WHERE chat_id=?`, chatID).Scan(&text)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return text, err
}

// SetSummary upserts the rolling summary for a chat.
func (s *Store) SetSummary(chatID int64, text string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO summaries(chat_id, text, updated_ts) VALUES(?,?,?)
		 ON CONFLICT(chat_id) DO UPDATE SET text=excluded.text, updated_ts=excluded.updated_ts`,
		chatID, text, time.Now().Unix())
	return err
}

// RecentEpisodes returns the last n episodes for a chat in chronological order (oldest first).
// Used when rebuilding the rolling summary.
func (s *Store) RecentEpisodes(chatID int64, n int) ([]Episode, error) {
	if s == nil || n <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, user_text, assistant_text, ts FROM episodes
		 WHERE chat_id=? ORDER BY id DESC LIMIT ?`, chatID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var eps []Episode
	for rows.Next() {
		var e Episode
		if err := rows.Scan(&e.ID, &e.User, &e.Assistant, &e.TS); err != nil {
			return nil, err
		}
		e.ChatID = chatID
		eps = append(eps, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Query is newest-first; reverse to chronological.
	for i, j := 0, len(eps)-1; i < j; i, j = i+1, j-1 {
		eps[i], eps[j] = eps[j], eps[i]
	}
	return eps, nil
}

// CountEpisodes returns how many episodes are stored for a chat.
func (s *Store) CountEpisodes(chatID int64) (int, error) {
	if s == nil {
		return 0, nil
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE chat_id=?`, chatID).Scan(&n)
	return n, err
}

// Forget deletes all memory for a chat (wired to /reset).
func (s *Store) Forget(chatID int64) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM episodes WHERE chat_id=?`, chatID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM summaries WHERE chat_id=?`, chatID)
	return err
}

// ===== helpers =====

func encodeVec(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// tokenize lower-cases queryText and keeps distinct word-ish tokens of length ≥3 for keyword
// recall. Deliberately simple (no stemming) — it's a fallback for when embeddings are off.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 'а' && r <= 'я' || r == 'ё')
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		if len([]rune(f)) < 3 || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}
