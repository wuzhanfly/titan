package candidate

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"time"
)

func startTcpServer(address string) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		panic(err)
	}

	listen, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
	// close listener
	defer listen.Close()

	log.Infof("tcp_server listen on %s", address)
	for {
		conn, err := listen.AcceptTCP()
		if err != nil {
			log.Fatal(err)
			os.Exit(1)
		}

		// conn.SetReadBuffer(104857600)
		go handleMessage(conn)
	}
}

func handleMessage(conn *net.TCPConn) {
	var now = time.Now()
	var size = int64(0)
	var deviceID = ""

	defer func() {
		if r := recover(); r != nil {
			log.Infof("handleMessage recovered. Error:\n", r)
			return
		}

		conn.Close()
		duration := time.Now().Sub(now)
		bandwidth := float64(size) / float64(duration) * 1000000000
		log.Infof("size:%d, duration:%d, bandwidth:%f, deviceID:%s", size, duration, bandwidth, deviceID)
	}()

	// first item is device id
	buf, err := readItem(conn)
	if err != nil {
		log.Errorf("read deviceID error:%v", err)
		return
	}
	deviceID = string(buf)
	if len(deviceID) == 0 {
		log.Errorf("deviceID is empty")
		return
	}

	vb, ok := loadValidateBlockFromMap(deviceID)
	if !ok {
		log.Errorf("Candidate no wait for device %s", deviceID)
		return
	}

	if vb.conn != nil {
		log.Errorf("device %s aready connect", deviceID)
		return
	}
	vb.conn = conn

	log.Infof("edge node %s connect to candidate, testing bandwidth", deviceID)

	for {
		// next item is file content
		buf, err = readItem(conn)
		if err != nil {
			log.Infof("read item error:%v, deviceID:%s", err, deviceID)
			if vb.ch != nil {
				// notify waitblock to stop
				close(vb.ch)
				// log.Infof("tcp-server close channel %s", deviceID)
			}
			vb.conn = nil
			return
		}

		size += int64(len(buf))

		safeSend(vb.ch, buf)

	}
}

func safeSend(ch chan []byte, value []byte) (closed bool) {
	defer func() {
		if recover() != nil {
			closed = true
		}
	}()

	ch <- value  // panic if ch is closed
	return false // <=> closed = false; return
}

func readItem(conn net.Conn) ([]byte, error) {
	len, err := readContentLen(conn)
	if err != nil {
		log.Infof("read len error:%v", err)
		return nil, err
	}

	if len <= 0 {
		return []byte{}, nil
	}

	buf, err := readContent(conn, len)
	if err != nil {
		log.Infof("read content error:%v", err)
		return nil, err
	}

	return buf, nil
}

func readContentLen(conn net.Conn) (int, error) {
	buffer := make([]byte, 4)
	_, err := conn.Read(buffer)
	if err != nil {
		return 0, err
	}

	var contentLen int32
	err = binary.Read(bytes.NewReader(buffer), binary.LittleEndian, &contentLen)
	if err != nil {
		return 0, err
	}

	return int(contentLen), nil
}

func readContent(conn net.Conn, conotentLen int) ([]byte, error) {
	content := make([]byte, 0, conotentLen)
	var readLen = 128
	for {
		if conotentLen-len(content) < 128 {
			readLen = conotentLen - len(content)
		}

		buffer := make([]byte, readLen)
		n, err := conn.Read(buffer)
		if err != nil {
			return nil, err
		}

		if n == 0 {
			return nil, fmt.Errorf("Content len not match, content len:%d, current read len:%d", conotentLen, len(content)+n)
		}

		if len(content)+n > conotentLen {
			return nil, fmt.Errorf("Content len not match, content len:%d, current read len:%d", conotentLen, len(content)+n)
		}

		if n > 0 {
			content = append(content, buffer[0:n]...)
		}

		if len(content) == conotentLen {
			return content, nil
		}
	}

}