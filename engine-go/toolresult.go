package main

// ToolResult (ТЗ §4.2). Dispatch used to return (string, error) and the agent glued failures
// into the text as «Ошибка инструмента X: …» — so the model could not tell "упало" from
// "запрещено" from "убито по таймауту", and simply called the tool again.
//
// Now the status is a value. Only Status + Text ever reach the model; artifacts go to the
// human and Err goes to the log (llama-server mixes images and tool schemas badly, §7).

import (
	"context"
	"errors"
	"strings"
)

// ResultStatus is what happened to one tool call.
type ResultStatus string

const (
	StatusOK        ResultStatus = "ok"
	StatusFailed    ResultStatus = "failed"
	StatusDenied    ResultStatus = "denied"
	StatusBlocked   ResultStatus = "blocked"
	StatusTimeout   ResultStatus = "timeout"
	StatusCancelled ResultStatus = "cancelled"
	StatusDeferred  ResultStatus = "deferred"
)

// ToolResult is one executed (or refused) tool call.
type ToolResult struct {
	Text      string       // THE ONLY thing that goes into the model's context
	Status    ResultStatus // ok | failed | denied | blocked | timeout | cancelled | deferred
	Artifacts []string     // files for the human, never for the context
	Err       error        // logs only
}

// timeoutToolText is deliberate wording (§4.2): after a kill the state is UNKNOWN, and the
// worst thing the model can do is blindly repeat the same command.
const timeoutToolText = "timeout: процесс убит по таймауту. Состояние неизвестно — команда могла успеть выполниться. " +
	"НЕ повторяй тот же вызов, проверь результат (например, прочитай файл или посмотри список процессов)."

// ModelText is what the `tool` message carries: the status word first, so the model reacts to
// the outcome and not just to the prose.
func (r ToolResult) ModelText() string {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		switch r.Status {
		case StatusOK:
			text = "(пусто)"
		default:
			text = "нет деталей"
		}
	}
	return string(r.Status) + ": " + text
}

// okResult / failResult / … are the small constructors used by the executor.
func okResult(text string, artifacts []string) ToolResult {
	return ToolResult{Text: text, Status: StatusOK, Artifacts: artifacts}
}

func failResult(text string, err error) ToolResult {
	return ToolResult{Text: text, Status: StatusFailed, Err: err}
}

func deniedResult(reason string) ToolResult {
	return ToolResult{
		Status: StatusDenied,
		Text: "запрещено политикой безопасности: " + reason +
			". В безопасном режиме это действие недоступно — предложи пользователю альтернативу или попроси включить полные руки.",
	}
}

func blockedResult(reason string) ToolResult {
	return ToolResult{
		Status: StatusBlocked,
		Text:   "заблокировано навсегда: " + reason + ". Это нельзя выполнить ни в каком режиме.",
	}
}

func deferredResult() ToolResult {
	return ToolResult{
		Status: StatusDeferred,
		Text:   "отложено: жду подтверждения пользователя по другому вызову этого хода. Ничего не выполнено.",
	}
}

// classifyToolErr maps an execution error onto a status.
//
// `cancelled` is claimed ONLY when the TASK context is actually done. A wrapped
// context.Canceled coming out of a library is not evidence that the user pressed /stop: on the
// first live run a stale chromedp page context made get_text return «context canceled», the
// tool was labelled cancelled, and the model dutifully told the user «выполнение прервано
// пользователем» — who had done nothing of the sort. A wrong status is worse than a vague one:
// the model reasons on it and then explains it to a human.
func classifyToolErr(taskCtx context.Context, text string, err error) ToolResult {
	taskDone := taskCtx != nil && taskCtx.Err() != nil
	switch {
	case err == nil:
		return okResult(text, nil)
	case taskDone && errors.Is(taskCtx.Err(), context.DeadlineExceeded):
		return ToolResult{Status: StatusTimeout, Text: timeoutToolText, Err: err}
	case taskDone:
		return ToolResult{Status: StatusCancelled, Text: "cancelled: выполнение прервано пользователем.", Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return ToolResult{Status: StatusTimeout, Text: timeoutToolText, Err: err}
	default:
		msg := err.Error()
		if text != "" {
			msg = msg + "\n" + text
		}
		return failResult(msg, err)
	}
}
