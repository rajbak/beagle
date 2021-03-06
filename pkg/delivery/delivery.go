package delivery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/blent/beagle/pkg/discovery/peripherals"
	"github.com/blent/beagle/pkg/notification"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type (
	Event struct {
		Name       string
		Timestamp  time.Time
		TargetName string
		Subscriber *notification.Subscriber
		Delivered  bool
		Error      error
	}

	EventListener func(evt Event)

	Sender struct {
		logger    *zap.Logger
		transport Transport
		listeners []EventListener
	}
)

func New(logger *zap.Logger, transport Transport) *Sender {
	return &Sender{
		logger,
		transport,
		make([]EventListener, 0, 5),
	}
}

func (sender *Sender) Send(msg *notification.Message) error {
	if !sender.isSupportedEventName(msg.EventName()) {
		return fmt.Errorf("%s %s", ErrUnsupportedEventName, msg.EventName())
	}

	// Call endpoints in batch inside a separate goroutine
	go sender.sendBatch(msg)

	return nil
}

func (sender *Sender) AddEventListener(listener EventListener) {
	if listener == nil {
		return
	}

	sender.listeners = append(sender.listeners, listener)
}

func (sender *Sender) RemoveEventListener(listener EventListener) bool {
	if listener == nil {
		return false
	}

	idx := -1
	handlerPointer := reflect.ValueOf(listener).Pointer()

	for i, element := range sender.listeners {
		currentPointer := reflect.ValueOf(element).Pointer()

		if currentPointer == handlerPointer {
			idx = i
		}
	}

	if idx < 0 {
		return false
	}

	sender.listeners = append(sender.listeners[:idx], sender.listeners[idx+1:]...)

	return true
}

func (sender *Sender) isSupportedEventName(name string) bool {
	if name == "" {
		return false
	}

	return name == "found" || name == "lost"
}

func (sender *Sender) sendBatch(msg *notification.Message) {
	subscribers := msg.Subscribers()
	events := make([]*Event, 0, len(subscribers))

	for _, subscriber := range subscribers {
		err := sender.sendSingle(msg.TargetName(), msg.Peripheral(), subscriber)

		evt := &Event{
			Name:       msg.EventName(),
			Timestamp:  time.Now(),
			TargetName: msg.TargetName(),
			Subscriber: subscriber,
			Delivered:  err == nil,
			Error:      err,
		}

		events = append(events, evt)

		if err == nil {
			sender.logger.Info(
				"Succeeded to notify a subscriber for peripheral",
				zap.String("subscriber", subscriber.Name),
				zap.String("peripheral", msg.TargetName()),
			)
		} else {
			sender.logger.Info(
				"Failed to notify a subscriber '%s' for peripheral '%s'",
				zap.String("subscriber", subscriber.Name),
				zap.String("peripheral", msg.TargetName()),
				zap.Error(err),
			)
		}
	}

	sender.emit(events)
}

func (sender *Sender) sendSingle(name string, peripheral peripherals.Peripheral, subscriber *notification.Subscriber) error {
	serialized, err := sender.serializePeripheral(name, peripheral)

	if err != nil {
		sender.logger.Error(err.Error())
		return err
	}

	endpoint := subscriber.Endpoint

	if endpoint == nil {
		sender.logger.Warn(
			"subscriber has no endpoints",
			zap.String("subscriber", subscriber.Name),
		)
		return nil
	}

	if endpoint.Url == "" {
		err = errors.New("Endpoint has an empty url")

		sender.logger.Error(
			"endpoint has an empty url: %s",
			zap.String("endpoint", endpoint.Name),
			zap.Error(err),
		)

		return err
	}

	method := strings.ToUpper(endpoint.Method)
	req, err := http.NewRequest(method, subscriber.Endpoint.Url, nil)

	if err != nil {
		sender.logger.Error(
			"failed to create a new request",
			zap.Error(err),
			zap.String("endpoint", endpoint.Name),
		)

		return errors.Wrap(err, "failed to create a new request")
	}

	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")

		body, err := json.Marshal(serialized)

		if err != nil {
			return err
		}

		req.Body = ioutil.NopCloser(bytes.NewReader(body))
	} else {
		query, err := sender.encode(serialized)

		if err != nil {
			return err
		}

		req.URL.RawQuery = query
	}

	if req == nil {
		err = fmt.Errorf(
			"%s: %s for endpoint %s",
			ErrUnsupportedHttpMethod,
			endpoint.Method,
			endpoint.Name,
		)

		sender.logger.Error(
			"Failed to create a request",
			zap.String("endpoint", endpoint.Name),
			zap.Error(err),
		)

		return err
	}

	headers := endpoint.Headers

	if headers != nil && len(headers) > 0 {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
	}

	err = sender.transport.Do(req)

	if err != nil {
		sender.logger.Error(
			"Failed to reach out the endpoint",
			zap.String("endpoint name", endpoint.Name),
			zap.String("endpoint url", endpoint.Url),
			zap.Error(err),
		)

		return err
	}

	return nil
}

func (sender *Sender) serializePeripheral(name string, peripheral peripherals.Peripheral) (map[string]interface{}, error) {
	if peripheral == nil {
		return nil, errors.New("missed peripheral")
	}

	serialized := make(map[string]interface{})

	serialized["name"] = name
	serialized["kind"] = peripheral.Kind()
	serialized["proximity"] = peripheral.Proximity()
	serialized["accuracy"] = strconv.FormatFloat(peripheral.Accuracy(), 'f', 6, 64)

	switch peripheral.Kind() {
	case peripherals.PERIPHERAL_IBEACON:
		ibeacon, ok := peripheral.(*peripherals.IBeaconPeripheral)

		if !ok {
			return nil, fmt.Errorf("%s %s", ErrUnableToSerializePeripheral, peripheral.UniqueKey())
		}

		serialized["uuid"] = ibeacon.Uuid()
		serialized["major"] = strconv.Itoa(int(ibeacon.Major()))
		serialized["minor"] = strconv.Itoa(int(ibeacon.Minor()))
	}

	return serialized, nil
}

func (sender *Sender) encode(data map[string]interface{}) (string, error) {
	var buf bytes.Buffer

	for k, v := range data {
		buf.WriteString(url.QueryEscape(k))
		buf.WriteByte('=')
		buf.WriteString(fmt.Sprintf("%s", v))
		buf.WriteByte('&')
	}

	str := buf.String()

	// remove last ampersand
	return str[0 : len(str)-1], nil
}

func (sender *Sender) emit(events []*Event) {
	if events == nil || len(events) == 0 {
		return
	}

	for _, listener := range sender.listeners {
		for _, evt := range events {
			listener(*evt)
		}
	}
}
