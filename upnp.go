package main

// Just enough UPnP to be able to forward ports
//

import (
	"bytes"
	"jackpal/http"
	"log"
	"os"
	"net"
	"strings"
	"strconv"
	"xml"
)

type upnpNAT struct {
	serviceURL string
}

type NAT interface {
	ForwardPort(protocol string, externalPort, internalPort int, description string, timeout int) (err os.Error)
	DeleteForwardingRule(protocol string, externalPort int) (err os.Error)
}

func Discover() (nat NAT, err os.Error) {
	ssdp, err := net.ResolveUDPAddr("239.255.255.250:1900")
	if err != nil {
		return
	}
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return
	}
	socket := conn.(*net.UDPConn)

	err = socket.SetReadTimeout(3 * 1000 * 1000 * 1000)
	if err != nil {
		return
	}

    st := "ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n"
	buf := bytes.NewBufferString(
		"M-SEARCH * HTTP/1.1\r\n" +
			"HOST: 239.255.255.250:1900\r\n" +
			 st +
			"MAN: \"ssdp:discover\"\r\n" +
			"MX: 2\r\n\r\n")
	message := buf.Bytes()
	answerBytes := make([]byte, 1024)
	for i := 0; i < 3; i++ {
		_, err = socket.WriteToUDP(message, ssdp)
		if err != nil {
			return
		}
		var n int
		n, _, err = socket.ReadFromUDP(answerBytes)
		if err != nil {
			continue
			// socket.Close()
			// return
		}
		answer := string(answerBytes[0:n])
		if strings.Index(answer, "\r\n" + st) < 0 {
			continue
		}
		locString := "\r\nLocation: "
		locIndex := strings.Index(answer, locString)
		if locIndex < 0 {
			continue
		}
		loc := answer[locIndex+len(locString):]
		endIndex := strings.Index(loc, "\r\n")
		if endIndex < 0 {
			continue
		}
		locURL := loc[0:endIndex]
		var serviceURL string
		serviceURL, err = getServiceURL(locURL)
		if err != nil {
			return
		}
		nat = &upnpNAT{serviceURL: serviceURL}
		break
	}
	socket.Close()
	return
}

type Service struct {
	ServiceType string
	ControlURL  string
}

type DeviceList struct {
	Device []Device
}

type ServiceList struct {
	Service []Service
}

type Device struct {
	DeviceType  string
	DeviceList  DeviceList
	ServiceList ServiceList
}

type Root struct {
	Device Device
}

func getChildDevice(d *Device, deviceType string) *Device {
	dl := d.DeviceList.Device
	for i := 0; i < len(dl); i++ {
		if dl[i].DeviceType == deviceType {
			return &dl[i]
		}
	}
	return nil
}

func getChildService(d *Device, serviceType string) *Service {
	sl := d.ServiceList.Service
	for i := 0; i < len(sl); i++ {
		if sl[i].ServiceType == serviceType {
			return &sl[i]
		}
	}
	return nil
}

func getServiceURL(rootURL string) (url string, err os.Error) {
	r, _, err := http.Get(rootURL)
	if err != nil {
		return
	}
	defer r.Body.Close()
	if r.StatusCode >= 400 {
		err = os.NewError(string(r.StatusCode))
		return
	}
	var root Root
	err = xml.Unmarshal(r.Body, &root)
	if err != nil {
		return
	}
	a := &root.Device
	if a.DeviceType != "urn:schemas-upnp-org:device:InternetGatewayDevice:1" {
		err = os.NewError("No InternetGatewayDevice")
		return
	}
	b := getChildDevice(a, "urn:schemas-upnp-org:device:WANDevice:1")
	if b == nil {
		err = os.NewError("No WANDevice")
		return
	}
	c := getChildDevice(b, "urn:schemas-upnp-org:device:WANConnectionDevice:1")
	if c == nil {
		err = os.NewError("No WANConnectionDevice")
		return
	}
	d := getChildService(c, "urn:schemas-upnp-org:service:WANIPConnection:1")
	if d == nil {
		err = os.NewError("No WANIPConnection")
		return
	}
	url = combineURL(rootURL, d.ControlURL)
	return
}

func combineURL(rootURL, subURL string) string {
	protocolEnd := "://"
	protoEndIndex := strings.Index(rootURL, protocolEnd)
	a := rootURL[protoEndIndex+len(protocolEnd):]
	rootIndex := strings.Index(a, "/")
	return rootURL[0:protoEndIndex+len(protocolEnd)+rootIndex] + subURL
}

type stringBuffer struct {
	base    string
	current string
}

func NewStringBuffer(s string) *stringBuffer { return &stringBuffer{s, s} }

func (sb *stringBuffer) Read(p []byte) (n int, err os.Error) {
	s := sb.current
	lenStr := len(s)
	if lenStr == 0 {
		return 0, os.EOF
	}
	n = len(p)
	if n > lenStr {
		n = lenStr
		err = os.EOF
	}
	for i := 0; i < n; i++ {
		p[i] = s[i]
	}
	sb.current = s[n:]
	return
}

func (sb *stringBuffer) Seek(offset int64, whence int) (ret int64, err os.Error) {
	var newOffset int64
	switch whence {
	case 0: // from beginning
		newOffset = offset
	case 1: // relative
		newOffset = int64(len(sb.base)-len(sb.current)) + offset
	case 2: // from end
		newOffset = int64(len(sb.base)) - offset
	default:
		err = os.NewError("bad whence")
		return
	}
	if newOffset < 0 || newOffset > int64(len(sb.base)) {
		err = os.NewError("offset out of range")
	} else {
		sb.current = sb.base[newOffset:]
		ret = newOffset
	}
	return
}

func soapRequest(url, function, message string) (r *http.Response, err os.Error) {
	fullMessage := "<?xml version=\"1.0\"?>" +
		"<s:Envelope xmlns:s=\"http://schemas.xmlsoap.org/soap/envelope/\" s:encodingStyle=\"http://schemas.xmlsoap.org/soap/encoding/\">\r\n" +
		"<s:Body>" + message + "</s:Body></s:Envelope>"

	var req http.Request
	req.Method = "POST"
	req.Body = NewStringBuffer(fullMessage)
	req.UserAgent = "Darwin/10.0.0, UPnP/1.0, MiniUPnPc/1.3"
	req.Header = map[string]string{
		"Content-Type": "text/xml",
		// "Transfer-Encoding": "chunked",
		"SOAPAction": "\"urn:schemas-upnp-org:service:WANIPConnection:1#" + function + "\"",
		"Connection": "Close",
		"Cache-Control": "no-cache",
		"Pragma": "no-cache",
	}

	req.URL, err = http.ParseURL(url)
	if err != nil {
		return
	}

	// log.Stderr("soapRequest ", req)

	r, err = http.Send(&req)

	if r.StatusCode >= 4000 {
		err = os.NewError("Error " + strconv.Itoa(r.StatusCode) + " for " + function)
		r.Body.Close()
		r = nil
		return
	}
	return
}

func (n *upnpNAT) ForwardPort(protocol string, externalPort, internalPort int, description string, timeout int) (err os.Error) {
	message := "<u:AddPortMapping xmlns:u=\"urn:schemas-upnp-org:service:WANIPConnection:1\">\r\n" +
		"<NewRemoteHost></NewRemoteHost><NewExternalPort>" + strconv.Itoa(externalPort) +
		"</NewExternalPort><NewProtocol>" + protocol + "</NewProtocol>" +
		"<NewInternalPort>" + strconv.Itoa(internalPort) + "</NewInternalPort><NewInternalClient>" +
		"192.168.0.124" + // TODO: Put our IP address here.
		"</NewInternalClient><NewEnabled>1</NewEnabled><NewPortMappingDescription>" +
		description +
		"</NewPortMappingDescription><NewLeaseDuration>" + strconv.Itoa(timeout) + "</NewLeaseDuration></u:AddPortMapping>"

	var response *http.Response
	response, err = soapRequest(n.serviceURL, "AddPortMapping", message)

	// TODO: check response to see if the port was forwarded
	_ = response
	return
}

func (n *upnpNAT) DeleteForwardingRule(protocol string, externalPort int) (err os.Error) {
	return
}

func testUPnP() {
	log.Stderr("Starting UPnP test")
	nat, err := Discover()
	log.Stderr("nat ", nat, "err ", err)
	if err != nil {
		return
	}
	port := 60001
	err = nat.ForwardPort("TCP", port, port, "Taipei-Torrent", 0)
	log.Stderr("err ", err)
	err = nat.DeleteForwardingRule("TCP", port)
	log.Stderr("err ", err)
}
