package worker

import (
	"fmt"
	"time"
)

// Options задаёт число обработчиков, таймауты и политику повторных попыток.
type Options struct {
	Count          int
	MaxAttempts    int
	PollInterval   time.Duration
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	FetchTimeout   time.Duration
	DBTimeout      time.Duration
	Lease          time.Duration
}

func DefaultOptions() Options {
	return Options{
		Count: 1, MaxAttempts: 3,
		PollInterval: time.Second, RetryBaseDelay: time.Second, RetryMaxDelay: 30 * time.Second,
		FetchTimeout: 5 * time.Second, DBTimeout: 3 * time.Second, Lease: 30 * time.Second,
	}
}

func (o Options) Validate() error {
	if o.Count <= 0 {
		return fmt.Errorf("worker count must be positive")
	}
	if o.MaxAttempts <= 0 || int64(o.MaxAttempts) > 1<<31-1 {
		return fmt.Errorf("max attempts must fit a positive PostgreSQL integer")
	}
	if o.PollInterval <= 0 || o.FetchTimeout <= 0 || o.DBTimeout <= 0 {
		return fmt.Errorf("poll interval and timeouts must be positive")
	}
	if o.RetryBaseDelay < time.Microsecond || o.RetryMaxDelay < o.RetryBaseDelay {
		return fmt.Errorf("retry delays must be at least one microsecond and max must be >= base")
	}
	// Аренда покрывает захват, Fetch и сохранение. Проверяем переполнение Duration.
	minimum := o.FetchTimeout + o.DBTimeout
	if minimum < o.FetchTimeout {
		return fmt.Errorf("worker timeout sum overflows")
	}
	minimumWithSave := minimum + o.DBTimeout
	if minimumWithSave < minimum || o.Lease <= minimumWithSave {
		return fmt.Errorf("lease must exceed fetch timeout plus two database timeouts")
	}
	return nil
}
