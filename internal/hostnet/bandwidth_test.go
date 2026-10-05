// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"encoding/binary"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	testTAP             = "tap-dicertest"
	testIFB             = "ifb-dicertest"
	testRateBps         = 100_000
	testBurstMultiplier = 4
	// testSendFor is how long a test sends for, far faster than the rate.
	testSendFor = 2 * time.Second
	// testDrainFor is how long a test then waits for what is queued to pass.
	testDrainFor = time.Second
)

// TestUploadLimitShapesWhatTheGuestSends writes frames into a TAP device as
// its guest would, and checks that what leaves the IFB device keeps to the
// rate.
func TestUploadLimitShapesWhatTheGuestSends(t *testing.T) {
	skipUnlessRoot(t)

	guest := openGuestSide(t, testTAP)
	t.Cleanup(func() {
		if err := removeUploadLimit(testIFB); err != nil {
			t.Errorf("removeUploadLimit: %v", err)
		}
		if _, err := netlink.LinkByName(testIFB); !isLinkNotFound(err) {
			t.Errorf("IFB %s is still there after removeUploadLimit", testIFB)
		}
	})
	if err := limitUpload(testTAP, testIFB, testRateBps, testBurstMultiplier); err != nil {
		t.Fatalf("limitUpload: %v", err)
	}

	frame := testFrame()
	var sent int
	for started := time.Now(); time.Since(started) < testSendFor; {
		n, err := guest.Write(frame)
		if err != nil {
			t.Fatalf("write a frame to %s: %v", testTAP, err)
		}
		sent += n
	}
	time.Sleep(testDrainFor)

	link, err := netlink.LinkByName(testIFB)
	if err != nil {
		t.Fatalf("look up %s: %v", testIFB, err)
	}
	checkRate(t, sent, int(link.Attrs().Statistics.TxBytes))
}

// TestDownloadLimitShapesWhatTheGuestReceives sends frames out of a TAP
// device as the host would, and checks that what its guest reads keeps to
// the rate.
func TestDownloadLimitShapesWhatTheGuestReceives(t *testing.T) {
	skipUnlessRoot(t)

	guest := openGuestSide(t, testTAP)
	if err := limitEgressRate(testTAP, testRateBps, testBurstMultiplier); err != nil {
		t.Fatalf("limitEgressRate: %v", err)
	}
	received := readUntilDone(t, guest)

	link, err := netlink.LinkByName(testTAP)
	if err != nil {
		t.Fatalf("look up %s: %v", testTAP, err)
	}
	host, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, 0)
	if err != nil {
		t.Fatalf("open a packet socket: %v", err)
	}
	defer func() { _ = unix.Close(host) }()
	address := &unix.SockaddrLinklayer{Ifindex: link.Attrs().Index}

	// Frames the full queue refuses count as sent: refusing them is the
	// limit at work.
	frame := testFrame()
	var sent int
	for started := time.Now(); time.Since(started) < testSendFor; {
		err := unix.Sendto(host, frame, unix.MSG_DONTWAIT, address)
		if err != nil && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.ENOBUFS) {
			t.Fatalf("send a frame out of %s: %v", testTAP, err)
		}
		sent += len(frame)
	}
	time.Sleep(testDrainFor)

	checkRate(t, sent, received())
}

// checkRate fails the test unless passed, of the sent bytes, is what the
// test rate lets through while sending, give or take the bucket's burst, the
// queue and a frame either way.
func checkRate(t *testing.T, sent, passed int) {
	t.Helper()

	atRate := int(testRateBps * testSendFor / time.Second)
	most := atRate + testRateBps*testBurstMultiplier/kernelHZ + testRateBps/20 + 2*len(testFrame())
	least := atRate * 9 / 10
	switch {
	case sent <= most:
		t.Fatalf("only %d bytes were sent, too few to exceed the limit", sent)
	case passed > most:
		t.Errorf("%d bytes passed, more than the %d the limit allows", passed, most)
	case passed < least:
		t.Errorf("%d bytes passed, fewer than the %d the limit allows", passed, least)
	}
	t.Logf("sent %d bytes, %d passed; the limit allows %d to %d", sent, passed, least, most)
}

// readUntilDone reads what the host sends a TAP device's guest from the file
// openGuestSide returned, until the test ends. The function it returns counts
// the bytes read so far.
func readUntilDone(t *testing.T, guest *os.File) func() int {
	t.Helper()

	var (
		received atomic.Int64
		done     = make(chan struct{})
		stopped  = make(chan struct{})
	)
	fd := int(guest.Fd())
	go func() {
		defer close(stopped)
		buf := make([]byte, 65536)
		for {
			select {
			case <-done:
				return
			default:
			}
			// Poll rather than block in read, which would keep the device
			// from being deleted when the test closes the file.
			ready, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 100)
			if err != nil && !errors.Is(err, unix.EINTR) {
				return
			}
			if ready == 0 {
				continue
			}
			n, err := unix.Read(fd, buf)
			if err != nil {
				return
			}
			received.Add(int64(n))
		}
	}()
	t.Cleanup(func() {
		close(done)
		<-stopped
	})

	return func() int { return int(received.Load()) }
}

// testFrame returns a 1000-byte Ethernet broadcast frame of a local
// experimental EtherType, which nothing on the host answers.
func testFrame() []byte {
	frame := make([]byte, 1000)
	copy(frame, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(frame[6:], []byte{0x02, 0, 0, 0, 0, 1}) // a local unicast source
	binary.BigEndian.PutUint16(frame[12:], 0x88b5)
	return frame
}

func skipUnlessRoot(t *testing.T) {
	t.Helper()

	if os.Geteuid() != 0 {
		t.Skip("needs root, to create network devices")
	}
}

// openGuestSide creates a TAP device and returns the file a VMM would hold
// for it: frames written to it arrive at the host on the device, and frames
// the host sends out of the device are read from it. Closing it, which the
// test does when it ends, deletes the device.
func openGuestSide(t *testing.T, name string) *os.File {
	t.Helper()

	tun, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open /dev/net/tun: %v", err)
	}
	t.Cleanup(func() { _ = tun.Close() })

	request, err := unix.NewIfreq(name)
	if err != nil {
		t.Fatalf("interface request for %s: %v", name, err)
	}
	request.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(int(tun.Fd()), unix.TUNSETIFF, request); err != nil {
		t.Fatalf("create TAP %s: %v", name, err)
	}

	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("look up %s: %v", name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("set %s up: %v", name, err)
	}

	return tun
}
