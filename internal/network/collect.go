package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
)

const (
	procFileLimit      int64 = 1 << 20
	commandOutputLimit int64 = 1 << 20
	commandErrorLimit  int64 = 64 << 10
)

type Interface struct {
	Name          string    `json:"name"`
	State         string    `json:"state,omitempty"`
	HardwareAddr  string    `json:"hardware_address,omitempty"`
	Addresses     []Address `json:"addresses"`
	ReceiveBytes  uint64    `json:"receive_bytes,omitempty"`
	TransmitBytes uint64    `json:"transmit_bytes,omitempty"`
}

type Address struct {
	Family string `json:"family"`
	Local  string `json:"local"`
	Prefix int    `json:"prefix"`
}

type Route struct {
	Family      string `json:"family"`
	Destination string `json:"destination"`
	Gateway     string `json:"gateway,omitempty"`
	Interface   string `json:"interface,omitempty"`
	Metric      int    `json:"metric,omitempty"`
}

type Listener struct {
	Protocol     string `json:"protocol"`
	LocalAddress string `json:"local_address"`
	Port         int    `json:"port"`
	State        string `json:"state"`
}

func collectInterfaces(ctx context.Context, local probe.Local) ([]Interface, error) {
	data, readErr := readProcNet(local, "dev")
	if readErr == nil {
		interfaces, parseErr := parseProcInterfaces(data)
		if parseErr == nil {
			enrichInterfacesFromSys(local, interfaces)
			return sanitizeInterfaces(interfaces), nil
		}
		readErr = parseErr
	}

	output, runErr := local.Run(ctx, probe.Command{
		Program: "ip", Arguments: []string{"-j", "address", "show"},
		StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit,
	})
	if runErr != nil {
		return nil, errors.Join(readErr, runErr)
	}
	if output.ExitCode != 0 || output.StdoutTruncated {
		return nil, errors.Join(readErr, fmt.Errorf("ip address probe failed"))
	}
	interfaces, parseErr := parseIPInterfaces(output.Stdout)
	if parseErr != nil {
		return nil, errors.Join(readErr, parseErr)
	}
	return sanitizeInterfaces(interfaces), nil
}

func enrichInterfacesFromSys(local probe.Local, interfaces []Interface) {
	for index := range interfaces {
		base := filepath.Join("sys", "class", "net", interfaces[index].Name)
		if data, err := local.Read(filepath.Join(base, "operstate"), 4<<10); err == nil {
			state := strings.ToLower(strings.TrimSpace(string(data)))
			if validState(state) {
				interfaces[index].State = state
			}
		}
		if data, err := local.Read(filepath.Join(base, "address"), 4<<10); err == nil {
			value := strings.TrimSpace(string(data))
			if hardware, parseErr := net.ParseMAC(value); parseErr == nil {
				interfaces[index].HardwareAddr = hardware.String()
			}
		}
	}
}

func validState(value string) bool {
	switch value {
	case "unknown", "notpresent", "down", "lowerlayerdown", "testing", "dormant", "up":
		return true
	default:
		return false
	}
}

func collectRoutes(ctx context.Context, local probe.Local) ([]Route, error) {
	data, readErr := readProcNet(local, "route")
	if readErr == nil {
		routes, parseErr := parseProcRoutes(data)
		if parseErr == nil {
			return sanitizeRoutes(routes), nil
		}
		readErr = parseErr
	}

	output, runErr := local.Run(ctx, probe.Command{
		Program: "ip", Arguments: []string{"-j", "route", "show", "table", "all"},
		StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit,
	})
	if runErr != nil {
		return nil, errors.Join(readErr, runErr)
	}
	if output.ExitCode != 0 || output.StdoutTruncated {
		return nil, errors.Join(readErr, fmt.Errorf("ip route probe failed"))
	}
	routes, parseErr := parseIPRoutes(output.Stdout)
	if parseErr != nil {
		return nil, errors.Join(readErr, parseErr)
	}
	return sanitizeRoutes(routes), nil
}

func collectListeners(ctx context.Context, local probe.Local) ([]Listener, error) {
	var listeners []Listener
	successfulFiles := 0
	var readErrors []error
	for _, source := range []struct {
		path     string
		protocol string
	}{
		{"tcp", "tcp"},
		{"tcp6", "tcp6"},
		{"udp", "udp"},
		{"udp6", "udp6"},
	} {
		data, err := readProcNet(local, source.path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				readErrors = append(readErrors, err)
			}
			continue
		}
		items, err := parseProcSockets(source.protocol, data)
		if err != nil {
			readErrors = append(readErrors, err)
			continue
		}
		successfulFiles++
		listeners = append(listeners, items...)
	}
	if successfulFiles > 0 {
		return sanitizeListeners(listeners), nil
	}

	output, runErr := local.Run(ctx, probe.Command{
		Program: "ss", Arguments: []string{"-H", "-l", "-n", "-t", "-u"},
		StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit,
	})
	if runErr != nil {
		return nil, errors.Join(append(readErrors, runErr)...)
	}
	if output.ExitCode != 0 || output.StdoutTruncated {
		return nil, errors.Join(append(readErrors, fmt.Errorf("ss listener probe failed"))...)
	}
	items, parseErr := parseSSListeners(output.Stdout)
	if parseErr != nil {
		return nil, errors.Join(append(readErrors, parseErr)...)
	}
	return sanitizeListeners(items), nil
}

func readProcNet(local probe.Local, leaf string) ([]byte, error) {
	var failures []error
	for _, relative := range []string{
		filepath.Join("proc", "net", leaf),
		filepath.Join("proc", strconv.Itoa(os.Getpid()), "net", leaf),
	} {
		data, err := local.Read(relative, procFileLimit)
		if err == nil {
			return data, nil
		}
		failures = append(failures, err)
	}
	return nil, errors.Join(failures...)
}

func sanitizeInterfaces(items []Interface) []Interface {
	output := append([]Interface(nil), items...)
	for index := range output {
		output[index].Name = redact.String(output[index].Name)
		output[index].State = redact.String(output[index].State)
		output[index].HardwareAddr = redact.String(output[index].HardwareAddr)
		output[index].Addresses = append([]Address(nil), output[index].Addresses...)
		for addressIndex := range output[index].Addresses {
			output[index].Addresses[addressIndex].Family =
				redact.String(output[index].Addresses[addressIndex].Family)
			output[index].Addresses[addressIndex].Local =
				redact.String(output[index].Addresses[addressIndex].Local)
		}
		sort.Slice(output[index].Addresses, func(left, right int) bool {
			if output[index].Addresses[left].Family == output[index].Addresses[right].Family {
				return output[index].Addresses[left].Local < output[index].Addresses[right].Local
			}
			return output[index].Addresses[left].Family < output[index].Addresses[right].Family
		})
		if output[index].Addresses == nil {
			output[index].Addresses = []Address{}
		}
	}
	sort.Slice(output, func(left, right int) bool { return output[left].Name < output[right].Name })
	return output
}

func sanitizeRoutes(items []Route) []Route {
	output := append([]Route(nil), items...)
	for index := range output {
		output[index].Family = redact.String(output[index].Family)
		output[index].Destination = redact.String(output[index].Destination)
		output[index].Gateway = redact.String(output[index].Gateway)
		output[index].Interface = redact.String(output[index].Interface)
	}
	sort.Slice(output, func(left, right int) bool {
		if output[left].Family != output[right].Family {
			return output[left].Family < output[right].Family
		}
		if output[left].Destination != output[right].Destination {
			return output[left].Destination < output[right].Destination
		}
		if output[left].Metric != output[right].Metric {
			return output[left].Metric < output[right].Metric
		}
		return output[left].Interface < output[right].Interface
	})
	return output
}

func sanitizeListeners(items []Listener) []Listener {
	output := append([]Listener(nil), items...)
	for index := range output {
		output[index].Protocol = redact.String(output[index].Protocol)
		output[index].LocalAddress = redact.String(output[index].LocalAddress)
		output[index].State = redact.String(output[index].State)
	}
	sort.Slice(output, func(left, right int) bool {
		if output[left].Protocol != output[right].Protocol {
			return output[left].Protocol < output[right].Protocol
		}
		if output[left].LocalAddress != output[right].LocalAddress {
			return output[left].LocalAddress < output[right].LocalAddress
		}
		return output[left].Port < output[right].Port
	})
	return output
}
