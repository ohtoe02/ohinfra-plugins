package network

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
)

var interfaceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,63}$`)

func parseProcInterfaces(data []byte) ([]Interface, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 {
		return nil, errors.New("malformed /proc/net/dev")
	}
	items := make([]Interface, 0, len(lines)-2)
	for _, line := range lines[2:] {
		name, counters, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errors.New("malformed interface row")
		}
		fields := strings.Fields(counters)
		if len(fields) != 16 {
			return nil, errors.New("malformed interface counters")
		}
		receive, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, errors.New("invalid receive counter")
		}
		transmit, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return nil, errors.New("invalid transmit counter")
		}
		name = strings.TrimSpace(name)
		if !interfaceName.MatchString(name) || strings.Contains(name, "..") {
			return nil, errors.New("invalid interface name")
		}
		items = append(items, Interface{
			Name: name, Addresses: []Address{},
			ReceiveBytes: receive, TransmitBytes: transmit,
		})
	}
	if len(items) == 0 {
		return nil, errors.New("no interface rows")
	}
	return items, nil
}

func parseIPInterfaces(data []byte) ([]Interface, error) {
	var decoded []struct {
		Name         string `json:"ifname"`
		State        string `json:"operstate"`
		HardwareAddr string `json:"address"`
		Addresses    []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
			Prefix int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := decodeBoundedJSON(data, &decoded); err != nil {
		return nil, fmt.Errorf("parse ip address JSON: %w", err)
	}
	items := make([]Interface, 0, len(decoded))
	for _, item := range decoded {
		if !interfaceName.MatchString(item.Name) || strings.Contains(item.Name, "..") {
			return nil, errors.New("ip address JSON contains an invalid interface name")
		}
		output := Interface{
			Name: item.Name, State: strings.ToLower(item.State),
			HardwareAddr: item.HardwareAddr, Addresses: []Address{},
		}
		for _, address := range item.Addresses {
			ip := net.ParseIP(address.Local)
			if ip == nil || address.Prefix < 0 ||
				(address.Family == "inet" && address.Prefix > 32) ||
				(address.Family == "inet6" && address.Prefix > 128) {
				return nil, errors.New("ip address JSON contains an invalid address")
			}
			family := address.Family
			if family != "inet" && family != "inet6" {
				continue
			}
			output.Addresses = append(output.Addresses, Address{
				Family: family, Local: ip.String(), Prefix: address.Prefix,
			})
		}
		items = append(items, output)
	}
	return items, nil
}

func parseProcRoutes(data []byte) ([]Route, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "Destination") {
		return nil, errors.New("malformed /proc/net/route header")
	}
	items := make([]Route, 0, len(lines)-1)
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			return nil, errors.New("malformed route row")
		}
		destination, err := parseProcIPv4(fields[1])
		if err != nil {
			return nil, err
		}
		gateway, err := parseProcIPv4(fields[2])
		if err != nil {
			return nil, err
		}
		mask, err := parseProcIPv4(fields[7])
		if err != nil {
			return nil, err
		}
		metric, err := strconv.Atoi(fields[6])
		if err != nil || metric < 0 {
			return nil, errors.New("invalid route metric")
		}
		prefix, _ := net.IPMask(net.ParseIP(mask).To4()).Size()
		destinationText := destination + "/" + strconv.Itoa(prefix)
		if destination == "0.0.0.0" && prefix == 0 {
			destinationText = "default"
		}
		if gateway == "0.0.0.0" {
			gateway = ""
		}
		items = append(items, Route{
			Family: "inet", Destination: destinationText,
			Gateway: gateway, Interface: fields[0], Metric: metric,
		})
	}
	return items, nil
}

func parseIPRoutes(data []byte) ([]Route, error) {
	var decoded []struct {
		Destination string `json:"dst"`
		Gateway     string `json:"gateway"`
		Device      string `json:"dev"`
		Metric      int    `json:"metric"`
	}
	if err := decodeBoundedJSON(data, &decoded); err != nil {
		return nil, fmt.Errorf("parse ip route JSON: %w", err)
	}
	items := make([]Route, 0, len(decoded))
	for _, item := range decoded {
		destination := item.Destination
		if destination == "" {
			destination = "default"
		}
		family := "inet"
		if strings.Contains(destination, ":") || strings.Contains(item.Gateway, ":") {
			family = "inet6"
		}
		if item.Gateway != "" && net.ParseIP(item.Gateway) == nil {
			return nil, errors.New("ip route JSON contains an invalid gateway")
		}
		if destination != "default" {
			if _, _, err := net.ParseCIDR(destination); err != nil {
				return nil, errors.New("ip route JSON contains an invalid destination")
			}
		}
		if item.Metric < 0 {
			return nil, errors.New("ip route JSON contains an invalid metric")
		}
		items = append(items, Route{
			Family: family, Destination: destination, Gateway: item.Gateway,
			Interface: item.Device, Metric: item.Metric,
		})
	}
	return items, nil
}

func parseProcSockets(protocolName string, data []byte) ([]Listener, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "local_address") {
		return nil, errors.New("malformed proc socket header")
	}
	items := make([]Listener, 0, len(lines)-1)
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, errors.New("malformed proc socket row")
		}
		address, port, err := parseProcEndpoint(protocolName, fields[1])
		if err != nil {
			return nil, err
		}
		state := strings.ToUpper(fields[3])
		switch {
		case strings.HasPrefix(protocolName, "tcp") && state != "0A":
			continue
		case strings.HasPrefix(protocolName, "udp") && state != "07" && state != "0A":
			continue
		}
		stateName := "listen"
		if strings.HasPrefix(protocolName, "udp") {
			stateName = "unconnected"
		}
		items = append(items, Listener{
			Protocol: protocolName, LocalAddress: address, Port: port, State: stateName,
		})
	}
	return items, nil
}

func parseSSListeners(data []byte) ([]Listener, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return []Listener{}, nil
	}
	lines := strings.Split(text, "\n")
	items := make([]Listener, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return nil, errors.New("malformed ss listener row")
		}
		address, port, err := parseSSEndpoint(fields[4])
		if err != nil {
			return nil, err
		}
		items = append(items, Listener{
			Protocol: strings.ToLower(fields[0]), State: strings.ToLower(fields[1]),
			LocalAddress: address, Port: port,
		})
	}
	return items, nil
}

func parseProcEndpoint(protocolName, value string) (string, int, error) {
	addressHex, portHex, ok := strings.Cut(value, ":")
	if !ok {
		return "", 0, errors.New("invalid proc endpoint")
	}
	port64, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", 0, errors.New("invalid proc port")
	}
	raw, err := hex.DecodeString(addressHex)
	if err != nil {
		return "", 0, errors.New("invalid proc address")
	}
	switch {
	case strings.HasSuffix(protocolName, "6"):
		if len(raw) != net.IPv6len {
			return "", 0, errors.New("invalid proc IPv6 address")
		}
		for offset := 0; offset < net.IPv6len; offset += 4 {
			raw[offset], raw[offset+3] = raw[offset+3], raw[offset]
			raw[offset+1], raw[offset+2] = raw[offset+2], raw[offset+1]
		}
	case len(raw) == net.IPv4len:
		binary.LittleEndian.PutUint32(raw, binary.BigEndian.Uint32(raw))
	default:
		return "", 0, errors.New("invalid proc IPv4 address")
	}
	return net.IP(raw).String(), int(port64), nil
}

func parseSSEndpoint(value string) (string, int, error) {
	index := strings.LastIndex(value, ":")
	if index < 0 {
		return "", 0, errors.New("invalid ss endpoint")
	}
	address := strings.Trim(value[:index], "[]")
	if address == "*" {
		address = "0.0.0.0"
	}
	port, err := strconv.Atoi(value[index+1:])
	if err != nil || port < 0 || port > 65535 {
		return "", 0, errors.New("invalid ss port")
	}
	if address != "0.0.0.0" && net.ParseIP(strings.Split(address, "%")[0]) == nil {
		return "", 0, errors.New("invalid ss address")
	}
	return address, port, nil
}

func parseProcIPv4(value string) (string, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != net.IPv4len {
		return "", errors.New("invalid proc IPv4 value")
	}
	raw[0], raw[3] = raw[3], raw[0]
	raw[1], raw[2] = raw[2], raw[1]
	return net.IP(raw).String(), nil
}

func decodeBoundedJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return errors.New("invalid trailing JSON data")
	}
	return nil
}
