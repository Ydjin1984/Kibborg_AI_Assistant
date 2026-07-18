// Package journal is Kibborg's trade journal: a SQLite record of every logged trade with its
// levels, size and outcome, plus aggregate stats (win-rate, expectancy, profit factor). It
// lets the agent learn from its OWN track record instead of guessing "я обычно прав" — the
// numbers come from real closed trades, computed here, never invented by the LLM.
//
// Driver: modernc.org/sqlite (pure Go, no cgo), same as the memory package.
package journal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Trade statuses.
const (
	StatusOpen      = "open"
	StatusWin       = "win"
	StatusLoss      = "loss"
	StatusBreakeven = "breakeven"
	StatusCancelled = "cancelled"
)

// Trade is one journalled trade.
type Trade struct {
	ID         int64     `json:"id"`
	ChatID     int64     `json:"chat_id"`
	Symbol     string    `json:"symbol"`
	Direction  string    `json:"direction"`
	Entry      float64   `json:"entry"`
	Stop       float64   `json:"stop"`
	Targets    []float64 `json:"targets"`
	Qty        float64   `json:"qty"`
	Notional   float64   `json:"notional"`
	Leverage   float64   `json:"leverage"`
	RiskAmount float64   `json:"risk_amount"`
	Regime     string    `json:"regime"`
	Score      float64   `json:"score"`
	Confidence float64   `json:"confidence"`
	Status     string    `json:"status"`
	Note       string    `json:"note"`
	OpenedTS   int64     `json:"opened_ts"`
	ClosedTS   int64     `json:"closed_ts"`
	ExitPrice  float64   `json:"exit_price"`
	PnL        float64   `json:"pnl"`
	RMultiple  float64   `json:"r_multiple"`
}

// Stats is the aggregate performance of a chat's closed trades.
type Stats struct {
	Total        int     `json:"total"` // closed trades counted
	Open         int     `json:"open"`  // still-open trades
	Wins         int     `json:"wins"`
	Losses       int     `json:"losses"`
	Breakeven    int     `json:"breakeven"`
	WinRate      float64 `json:"win_rate"`   // wins / (wins+losses), percent
	AvgR         float64 `json:"avg_r"`      // mean R-multiple of closed trades
	Expectancy   float64 `json:"expectancy"` // mean PnL per closed trade
	TotalPnL     float64 `json:"total_pnl"`
	ProfitFactor float64 `json:"profit_factor"` // gross win / gross loss (0 = n/a)
}

// Store wraps the SQLite journal database.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open opens (creating if needed) the journal database and ensures the schema.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS trades (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id     INTEGER NOT NULL,
  symbol      TEXT NOT NULL,
  direction   TEXT NOT NULL,
  entry       REAL NOT NULL,
  stop        REAL NOT NULL,
  targets     TEXT,
  qty         REAL,
  notional    REAL,
  leverage    REAL,
  risk_amount REAL,
  regime      TEXT,
  score       REAL,
  confidence  REAL,
  status      TEXT NOT NULL,
  note        TEXT,
  opened_ts   INTEGER NOT NULL,
  closed_ts   INTEGER,
  exit_price  REAL,
  pnl         REAL,
  r_multiple  REAL
);
CREATE INDEX IF NOT EXISTS idx_trades_chat ON trades(chat_id, id);`
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

// Add inserts a new open trade and returns its id. Status/opened_ts are set here.
func (s *Store) Add(t Trade) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("журнал недоступен")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`INSERT INTO trades(chat_id, symbol, direction, entry, stop, targets, qty, notional,
		 leverage, risk_amount, regime, score, confidence, status, note, opened_ts)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ChatID, t.Symbol, t.Direction, t.Entry, t.Stop, encodeFloats(t.Targets), t.Qty, t.Notional,
		t.Leverage, t.RiskAmount, t.Regime, t.Score, t.Confidence, StatusOpen, t.Note, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Close records an exit for an open trade, computing PnL, R-multiple and win/loss/breakeven.
// It returns the updated trade. Refuses to re-close an already-closed trade.
func (s *Store) CloseTrade(id int64, exitPrice float64) (Trade, error) {
	if s == nil {
		return Trade{}, fmt.Errorf("журнал недоступен")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getLocked(id)
	if err != nil {
		return Trade{}, err
	}
	if t.Status != StatusOpen {
		return Trade{}, fmt.Errorf("сделка #%d уже закрыта (статус %s)", id, t.Status)
	}
	if exitPrice <= 0 {
		return Trade{}, fmt.Errorf("нужна положительная цена выхода")
	}

	var perUnit float64
	if t.Direction == "long" {
		perUnit = exitPrice - t.Entry
	} else {
		perUnit = t.Entry - exitPrice
	}
	pnl := perUnit * t.Qty
	rMult := 0.0
	if t.RiskAmount > 0 {
		rMult = pnl / t.RiskAmount
	}
	status := StatusBreakeven
	// Treat a move smaller than 1% of the risk amount as breakeven to avoid float noise.
	eps := 0.01 * t.RiskAmount
	if pnl > eps {
		status = StatusWin
	} else if pnl < -eps {
		status = StatusLoss
	}

	now := time.Now().Unix()
	_, err = s.db.Exec(
		`UPDATE trades SET status=?, exit_price=?, pnl=?, r_multiple=?, closed_ts=? WHERE id=?`,
		status, exitPrice, round2(pnl), round2(rMult), now, id)
	if err != nil {
		return Trade{}, err
	}
	t.Status = status
	t.ExitPrice = exitPrice
	t.PnL = round2(pnl)
	t.RMultiple = round2(rMult)
	t.ClosedTS = now
	return t, nil
}

// Cancel marks an open trade as cancelled (never triggered / abandoned).
func (s *Store) Cancel(id int64) error {
	if s == nil {
		return fmt.Errorf("журнал недоступен")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE trades SET status=?, closed_ts=? WHERE id=? AND status=?`,
		StatusCancelled, time.Now().Unix(), id, StatusOpen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("открытая сделка #%d не найдена", id)
	}
	return nil
}

// Get returns one trade by id.
func (s *Store) Get(id int64) (Trade, error) {
	if s == nil {
		return Trade{}, fmt.Errorf("журнал недоступен")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(id)
}

func (s *Store) getLocked(id int64) (Trade, error) {
	row := s.db.QueryRow(`SELECT `+cols+` FROM trades WHERE id=?`, id)
	t, err := scanTrade(row)
	if err == sql.ErrNoRows {
		return Trade{}, fmt.Errorf("сделка #%d не найдена", id)
	}
	return t, err
}

// List returns a chat's trades, newest first. status="" lists all; limit≤0 → 20.
func (s *Store) List(chatID int64, status string, limit int) ([]Trade, error) {
	if s == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.Query(`SELECT `+cols+` FROM trades WHERE chat_id=? ORDER BY id DESC LIMIT ?`, chatID, limit)
	} else {
		rows, err = s.db.Query(`SELECT `+cols+` FROM trades WHERE chat_id=? AND status=? ORDER BY id DESC LIMIT ?`, chatID, status, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Trade
	for rows.Next() {
		t, err := scanTrade(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Stats aggregates a chat's performance over closed trades.
func (s *Store) Stats(chatID int64) (Stats, error) {
	if s == nil {
		return Stats{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT status, pnl, r_multiple FROM trades WHERE chat_id=?`, chatID)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	var st Stats
	var sumR, grossWin, grossLoss float64
	for rows.Next() {
		var status string
		var pnl, r sql.NullFloat64
		if err := rows.Scan(&status, &pnl, &r); err != nil {
			return Stats{}, err
		}
		switch status {
		case StatusOpen:
			st.Open++
			continue
		case StatusCancelled:
			continue
		}
		st.Total++
		st.TotalPnL += pnl.Float64
		sumR += r.Float64
		switch status {
		case StatusWin:
			st.Wins++
			grossWin += pnl.Float64
		case StatusLoss:
			st.Losses++
			grossLoss += pnl.Float64 // negative
		case StatusBreakeven:
			st.Breakeven++
		}
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	if decided := st.Wins + st.Losses; decided > 0 {
		st.WinRate = round2(float64(st.Wins) / float64(decided) * 100)
	}
	if st.Total > 0 {
		st.AvgR = round2(sumR / float64(st.Total))
		st.Expectancy = round2(st.TotalPnL / float64(st.Total))
	}
	if grossLoss != 0 {
		st.ProfitFactor = round2(grossWin / math.Abs(grossLoss))
	}
	st.TotalPnL = round2(st.TotalPnL)
	return st, nil
}

// ===== helpers =====

// cols is the shared column list for scanTrade (order matters).
const cols = `id, chat_id, symbol, direction, entry, stop, targets, qty, notional, leverage,
	risk_amount, regime, score, confidence, status, note, opened_ts, closed_ts, exit_price, pnl, r_multiple`

// rowScanner unifies *sql.Row and *sql.Rows for scanTrade.
type rowScanner interface{ Scan(dest ...any) error }

func scanTrade(r rowScanner) (Trade, error) {
	var t Trade
	var targets sql.NullString
	var note sql.NullString
	var regime sql.NullString
	var closedTS sql.NullInt64
	var qty, notional, leverage, riskAmount, score, confidence, exitPrice, pnl, rMult sql.NullFloat64
	err := r.Scan(&t.ID, &t.ChatID, &t.Symbol, &t.Direction, &t.Entry, &t.Stop, &targets, &qty, &notional,
		&leverage, &riskAmount, &regime, &score, &confidence, &t.Status, &note, &t.OpenedTS, &closedTS,
		&exitPrice, &pnl, &rMult)
	if err != nil {
		return Trade{}, err
	}
	t.Targets = decodeFloats(targets.String)
	t.Qty = qty.Float64
	t.Notional = notional.Float64
	t.Leverage = leverage.Float64
	t.RiskAmount = riskAmount.Float64
	t.Regime = regime.String
	t.Score = score.Float64
	t.Confidence = confidence.Float64
	t.Note = note.String
	t.ClosedTS = closedTS.Int64
	t.ExitPrice = exitPrice.Float64
	t.PnL = pnl.Float64
	t.RMultiple = rMult.Float64
	return t, nil
}

func encodeFloats(xs []float64) string {
	if len(xs) == 0 {
		return ""
	}
	b, _ := json.Marshal(xs)
	return string(b)
}

func decodeFloats(s string) []float64 {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var xs []float64
	_ = json.Unmarshal([]byte(s), &xs)
	return xs
}

func round2(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return math.Round(f*100) / 100
}
