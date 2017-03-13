package activity

import (
	"github.com/blent/beagle/src/core/discovery/peripherals"
	"github.com/blent/beagle/src/core/logging"
	"github.com/blent/beagle/src/core/notification"
	"sync"
	"time"
	"sort"
)

type Service struct {
	mu      *sync.RWMutex
	logger  *logging.Logger
	records map[string]*Record
}

func NewService(logger *logging.Logger) *Service {
	return &Service{
		mu: &sync.RWMutex{},
		logger:  logger,
		records: make(map[string]*Record),
	}
}

func (s *Service) Quantity() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.records)
}

func (s *Service) GetRecords(take, skip int) []*Record {
	s.mu.RLock()
	defer s.mu.RUnlock()

	resultSize := take

	if take == 0 {
		resultSize = len(s.records)
	}

	// convert map to list
	list := make([]*Record, 0, len(s.records))

	// TODO: Sort to keep slice' order
	result := make([]*Record, 0, resultSize)

	for _, record := range s.records {
		list = append(list, record)
	}

	for idx, record := range list {
		num := idx + 1
		if skip == 0 || skip > num  {
			if len(result) == resultSize {
				break
			}

			// copying..
			item := *record
			list = append(result, &item)
		}
	}

	return result
}

func (s *Service) Use(broker *notification.EventBroker) *Service {
	if broker == nil {
		return s
	}

	broker.Subscribe(notification.PERIPHERAL_FOUND, func(peripheral peripherals.Peripheral, registered bool) {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.records[peripheral.UniqueKey()] = &Record{
			Key:        peripheral.UniqueKey(),
			Kind:       peripheral.Kind(),
			Proximity:  peripheral.Proximity(),
			Registered: registered,
			Time:       time.Now(),
		}
	})

	broker.Subscribe(notification.PERIPHERAL_LOST, func(peripheral peripherals.Peripheral, registered bool) {
		s.mu.Lock()
		defer s.mu.Unlock()

		delete(s.records, peripheral.UniqueKey())
	})

	return s
}
