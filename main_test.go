package main // import "github.com/nutmegdevelopment/flowlog-exporter"

import (
	"bytes"
	"compress/gzip"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSubnetLookup(t *testing.T) {
	s := new(Subnets)

	_, n, err := net.ParseCIDR("10.1.2.0/24")
	assert.NoError(t, err)

	s.networks = append(s.networks, n)

	inside := net.ParseIP("10.1.2.3")
	outside := net.ParseIP("10.1.3.4")

	res, in := s.Lookup(inside)
	assert.True(t, in)
	assert.Equal(t, "10.1.2.0/24", res)

	res, in = s.Lookup(outside)
	assert.False(t, in)
	assert.Empty(t, res)
}

func TestDecompress(t *testing.T) {
	data := []byte("test data")

	buf := new(bytes.Buffer)
	w := gzip.NewWriter(buf)
	w.Write(data)
	w.Close()

	in := make(chan []byte)
	out := make(chan []byte)

	go Decompress(in, out)

	go func() {
		in <- buf.Bytes()
		close(in)
	}()

	res := <-out
	assert.Equal(t, data, res)
}

func TestDecode(t *testing.T) {
	data := []byte(`
    {"messageType":"DATA_MESSAGE","owner":"1234567890","logGroup":"network-logs","logStream":"eni-12345678-all","subscriptionFilters":["cloudwatch_to_kinesis_network_test"],"logEvents":[{"id":"32694987036237920317511708164876410007359838359118348288","timestamp":1466093924000,"message":"2 217746932756 eni-12345678 10.100.1.2 10.100.2.3 80 25901 6 2 112 1466176699 1466176812 ACCEPT OK"},{"id":"32694987036237920317511708164876410007359838359118348289","timestamp":1466093924000,"message":"2 217746932756 eni-12345678 10.100.1.2 10.100.2.4 80 25901 6 2 112 1466176699 1466176812 ACCEPT OK"}]}
    `)
	in := make(chan []byte)
	out := make(chan FlowLogEvent)

	go Decode(in, out)

	go func() {
		in <- data
		close(in)
	}()

	res := <-out
	assert.Equal(t, 2, len(res.LogEvents))
}

func TestParseMessage(t *testing.T) {

	testInput := `2 217746932756 eni-12345678 10.100.1.2 10.100.2.3 80 25901 6 2 112 1466176699 1466176812 ACCEPT OK`
	f, err := ParseMessage(testInput)
	assert.NoError(t, err)

	assert.True(t, f.Accepted)
	assert.Equal(t, "10.100.1.2", f.Src.String())
	assert.Equal(t, 80, f.SrcPort)
}
