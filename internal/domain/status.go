package domain

type Status string

const (
	StatusPending Status = "pending"

	StatusProcessing Status = "processing"

	StatusDone Status = "done"

	StatusFailed Status = "failed"
)

func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusFailed
}
