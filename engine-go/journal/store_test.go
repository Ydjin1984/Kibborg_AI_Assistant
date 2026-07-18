package journal

import (
	"math"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestCloseTradeComputesPnLAndR(t *testing.T) {
	st := openTemp(t)
	// Long: entry 100, qty 4, risk 20. Exit 110 → pnl (110-100)*4=40, R=40/20=2, win.
	id, err := st.Add(Trade{ChatID: 1, Symbol: "BTC", Direction: "long", Entry: 100, Stop: 95, Qty: 4, RiskAmount: 20})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := st.CloseTrade(id, 110)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != StatusWin {
		t.Errorf("статус=%s, ожидался win", tr.Status)
	}
	if !approx(tr.PnL, 40) {
		t.Errorf("pnl=%.4f, ожидалось 40", tr.PnL)
	}
	if !approx(tr.RMultiple, 2) {
		t.Errorf("R=%.4f, ожидалось 2", tr.RMultiple)
	}
	// Re-closing must fail.
	if _, err := st.CloseTrade(id, 120); err == nil {
		t.Error("повторное закрытие должно быть ошибкой")
	}
}

func TestCloseTradeShortLossAndBreakeven(t *testing.T) {
	st := openTemp(t)
	// Short loss: entry 100, qty 5, risk 50. Exit 110 → pnl (100-110)*5=-50, loss.
	id, _ := st.Add(Trade{ChatID: 1, Symbol: "ETH", Direction: "short", Entry: 100, Stop: 110, Qty: 5, RiskAmount: 50})
	tr, err := st.CloseTrade(id, 110)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != StatusLoss || !approx(tr.PnL, -50) {
		t.Errorf("ожидался loss -50, got %s %.4f", tr.Status, tr.PnL)
	}
	// Breakeven: exit == entry.
	id2, _ := st.Add(Trade{ChatID: 1, Symbol: "SOL", Direction: "long", Entry: 50, Stop: 48, Qty: 2, RiskAmount: 4})
	tr2, _ := st.CloseTrade(id2, 50)
	if tr2.Status != StatusBreakeven {
		t.Errorf("ожидался breakeven, got %s (pnl %.4f)", tr2.Status, tr2.PnL)
	}
}

func TestStatsAggregate(t *testing.T) {
	st := openTemp(t)
	const chat = int64(7)
	// Win +40 (R2), loss -20 (R-1), one still open.
	id1, _ := st.Add(Trade{ChatID: chat, Symbol: "BTC", Direction: "long", Entry: 100, Stop: 95, Qty: 4, RiskAmount: 20})
	st.CloseTrade(id1, 110) // +40
	id2, _ := st.Add(Trade{ChatID: chat, Symbol: "BTC", Direction: "long", Entry: 100, Stop: 95, Qty: 4, RiskAmount: 20})
	st.CloseTrade(id2, 95)                                                                                    // -20
	st.Add(Trade{ChatID: chat, Symbol: "ETH", Direction: "long", Entry: 50, Stop: 48, Qty: 1, RiskAmount: 2}) // open

	s, err := st.Stats(chat)
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 2 || s.Wins != 1 || s.Losses != 1 || s.Open != 1 {
		t.Errorf("total/wins/losses/open = %d/%d/%d/%d, ожидалось 2/1/1/1", s.Total, s.Wins, s.Losses, s.Open)
	}
	if !approx(s.WinRate, 50) {
		t.Errorf("winRate=%.2f, ожидалось 50", s.WinRate)
	}
	if !approx(s.TotalPnL, 20) { // +40 -20
		t.Errorf("totalPnL=%.4f, ожидалось 20", s.TotalPnL)
	}
	if !approx(s.ProfitFactor, 2) { // 40 / 20
		t.Errorf("profitFactor=%.4f, ожидалось 2", s.ProfitFactor)
	}
	if !approx(s.AvgR, 0.5) { // (2 + -1)/2
		t.Errorf("avgR=%.4f, ожидалось 0.5", s.AvgR)
	}
}

func TestCancelAndList(t *testing.T) {
	st := openTemp(t)
	const chat = int64(3)
	id, _ := st.Add(Trade{ChatID: chat, Symbol: "BTC", Direction: "long", Entry: 100, Stop: 95, Qty: 1, RiskAmount: 5})
	if err := st.Cancel(id); err != nil {
		t.Fatal(err)
	}
	// Cancelling again (no longer open) must fail.
	if err := st.Cancel(id); err == nil {
		t.Error("повторная отмена должна быть ошибкой")
	}
	open, _ := st.List(chat, StatusOpen, 10)
	if len(open) != 0 {
		t.Errorf("открытых сделок быть не должно, got %d", len(open))
	}
	all, _ := st.List(chat, "", 10)
	if len(all) != 1 {
		t.Errorf("всего сделок ожидалось 1, got %d", len(all))
	}
}
