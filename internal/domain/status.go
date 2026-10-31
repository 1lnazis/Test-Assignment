package domain

// Status — состояние запроса на обновление котировки на пути от создания до результата.
type Status string

const (
	// StatusPending — запрос принят и ждёт, пока его возьмёт воркер.
	StatusPending Status = "pending"
	// StatusProcessing — воркер взял запрос в работу.
	StatusProcessing Status = "processing"
	// StatusDone — цена получена и сохранена.
	StatusDone Status = "done"
	// StatusFailed — попытки закончились, причина сохранена вместе с запросом.
	StatusFailed Status = "failed"
)

// Terminal сообщает, конечный ли это статус, то есть дальше запрос уже не меняется.
func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusFailed
}
