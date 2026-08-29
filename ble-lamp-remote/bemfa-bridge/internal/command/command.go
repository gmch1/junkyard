package command

import (
	"errors"
)

var ErrUnsupported = errors.New("unsupported Bemfa lamp command")

type Command struct {
	Payload string
	Path    string
}

func Parse(payload []byte) (Command, error) {
	if len(payload) == 0 || len(payload) > 64 {
		return Command{}, ErrUnsupported
	}

	value := string(payload)
	switch value {
	case "on":
		return Command{Payload: "on", Path: "/v1/light/on"}, nil
	case "off":
		return Command{Payload: "off", Path: "/v1/light/off"}, nil
	default:
		return Command{}, ErrUnsupported
	}
}
