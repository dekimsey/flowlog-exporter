package main // import "github.com/nutmegdevelopment/flowlog-exporter"

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/kinesis"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/olivere/elastic.v2"
)

const esType = "flowlog_denials"

var (
	awsSession  *session.Session
	esClient    *elastic.Client
	getSize     int64 = 256
	esBatchSize       = 4096
	shards      int
	streamName  string
	port        string
	esURL       string
	esIndex     string
	logDeny     bool
	subnets     Subnets
	metrics     Metrics
)

func init() {

	flag.IntVar(&shards, "shards", 1, "Kinesis shard count")
	flag.StringVar(&streamName, "stream", "", "Kinesis stream name")
	flag.StringVar(&port, "port", "8080", "TCP port to listen on (defaults to 8080)")
	flag.BoolVar(&logDeny, "log_deny", false, "Log denied packets")
	region := flag.String("region", "eu-west-1", "Set AWS region")
	flag.StringVar(&esURL, "es_url", "", "If specified, sends the log_deny data to this elasticsearch endpoint")
	flag.StringVar(&esIndex, "es_index", "", "If using elasticsearch output, specifies the index name")

	debug := flag.Bool("debug", false, "Turn on debug mode")
	flag.Parse()

	awsSession = session.New(&aws.Config{Region: region})
	if *debug {
		log.SetLevel(log.DebugLevel)
	}
}

func esIndexName() string {
	t := time.Now()
	return fmt.Sprintf("%s-%s", esIndex, t.Format("2006-01-02"))
}

// Subnets is a list of all available AWS subnets that we have access to.
type Subnets struct {
	networks []*net.IPNet
	sync.Mutex
}

// Get updates the subnet list.
func (s *Subnets) Get() error {
	svc := ec2.New(awsSession)

	params := &ec2.DescribeSubnetsInput{

		Filters: []*ec2.Filter{
			{
				Name: aws.String("state"),
				Values: []*string{
					aws.String("available"),
				},
			},
		},
	}
	resp, err := svc.DescribeSubnets(params)
	if err != nil {
		return err
	}

	s.Lock()
	defer s.Unlock()
	subnetList := s.networks
	for i := range resp.Subnets {
		_, cidr, err := net.ParseCIDR(*resp.Subnets[i].CidrBlock)
		if err != nil {
			return err
		}
		subnetList = append(subnetList, cidr)
		log.Debugf("Registered local subnet: %s", cidr.String())
	}
	s.networks = subnetList
	return nil
}

// Lookup returns the subnet for an IP, or false if the subnet is not in the list.
func (s *Subnets) Lookup(ip net.IP) (string, bool) {
	s.Lock()
	defer s.Unlock()

	for i := range s.networks {
		if s.networks[i].Contains(ip) {
			return s.networks[i].String(), true
		}
	}
	return "", false
}

// Stream reads raw data from a kinesis stream into a channel.
func Stream(id int, out chan<- []byte) {
	shardID := strconv.Itoa(id)

	log.Debugf("Starting stream reader on shard %s", shardID)

	shard, err := getShardIterator(shardID)
	if err != nil {
		log.Error(err)
	}

	iter := shard.ShardIterator

	for {
		recs, err := getRecord(iter)
		if err != nil {
			log.Error(err)
		}

		for i := range recs.Records {
			if err != nil {
				log.Error(err)
				continue
			}
			out <- recs.Records[i].Data
		}

		iter = recs.NextShardIterator
	}
}

// Decompress expands the raw data from in, and passes it to out.
func Decompress(in <-chan []byte, out chan<- io.Reader) {
	for src := range in {
		buf, err := decompress(src)
		if err != nil {
			log.Error(err)
			continue
		}
		out <- buf
	}
	close(out)
}

func decompress(in []byte) (io.Reader, error) {
	return gzip.NewReader(bytes.NewReader(in))
}

// Decode parses JSON data from in into FlowLogEvents.
func Decode(in <-chan io.Reader, out chan<- FlowLogEvent) {
	for src := range in {
		buf, err := decode(src)
		if err != nil {
			log.Error(err)
			continue
		}
		out <- buf
	}
	close(out)
}

func decode(in io.Reader) (FlowLogEvent, error) {
	var buf FlowLogEvent
	err := json.NewDecoder(in).Decode(&buf)
	return buf, err
}

func getShardIterator(shardID string) (*kinesis.GetShardIteratorOutput, error) {
	svc := kinesis.New(awsSession)

	params := &kinesis.GetShardIteratorInput{
		ShardId:           aws.String(shardID),
		ShardIteratorType: aws.String("TRIM_HORIZON"),
		StreamName:        aws.String(streamName),
	}
	return svc.GetShardIterator(params)
}

func getRecord(iter *string) (*kinesis.GetRecordsOutput, error) {
	svc := kinesis.New(awsSession)

	params := &kinesis.GetRecordsInput{
		ShardIterator: iter,
		Limit:         aws.Int64(getSize),
	}
	return svc.GetRecords(params)

}

/*
version	The VPC flow logs version.
account-id	The AWS account ID for the flow log.
interface-id	The ID of the network interface for which the log stream applies.
srcaddr	The source IP address. The IP address of the network interface is always its private IP address.
dstaddr	The destination IP address. The IP address of the network interface is always its private IP address.
srcport	The source port of the traffic.
dstport	The destination port of the traffic.
protocol	The IANA protocol number of the traffic. For more information, go to Assigned Internet Protocol Numbers.
packets	The number of packets transferred during the capture window.
bytes	The number of bytes transferred during the capture window.
start	The time, in Unix seconds, of the start of the capture window.
end	The time, in Unix seconds, of the end of the capture window.
action	The action associated with the traffic:
  ACCEPT: The recorded traffic was permitted by the security groups or network ACLs.
  REJECT: The recorded traffic was not permitted by the security groups or network ACLs.
log-status	The logging status of the flow log:
  OK: Data is logging normally to CloudWatch Logs.
  NODATA: There was no network traffic to or from the network interface during the capture window.
  SKIPDATA: Some flow log records were skipped during the capture window. This may be because of an internal capacity constraint, or an internal error.
*/

// FlowMessage describes some of the fields exported in flow logs.  See above for full format description.
type FlowMessage struct {
	Src       net.IP    `json:"src"`
	Dst       net.IP    `json:"dst"`
	SrcPort   int       `json:"srcport"`
	DstPort   int       `json:"dstport"`
	Protocol  string    `json:"protocol"`
	Packets   float64   `json:"packets"`
	Bytes     float64   `json:"-"`
	Accepted  bool      `json:"-"`
	Timestamp time.Time `json:"timestamp"`
}

// MessageMapping is the elasticsearch mapping for a FlowMessage.
var MessageMapping = `
{
	"properties": {
		"src": {
			"type": "ip"
		},
		"dst": {
			"type": "ip"
		},
		"srcport": {
			"type": "integer"
		},
		"dstport": {
			"type": "integer"
		},
		"protocol": {
			"type": "string"
		},
		"packets": {
			"type": "double"
		},
		"timestamp": {
			"type": "date"
		}
	}
}
`

var protocols = map[string]string{
	"1":  "ICMP",
	"2":  "IGMP",
	"6":  "TCP",
	"17": "UDP",
	"41": "IPV6-ENCAP",
	"58": "IPV6-ICMP",
}

// ParseMessage splits a message into a FlowMessage struct
func ParseMessage(message string) (f FlowMessage, err error) {
	arr := strings.Split(message, " ")

	if len(arr) != 14 {
		log.Debugf("Message: %s has %d terms", message, len(arr))
		return f, errors.New("Bad record length")
	}

	if arr[13] != "OK" {
		log.Debugf("Message status is %s", arr[13])
		return f, errors.New("Incomplete record")
	}

	f.Src = net.ParseIP(arr[3])
	f.Dst = net.ParseIP(arr[4])
	f.SrcPort, err = strconv.Atoi(arr[5])
	if err != nil {
		return
	}
	f.DstPort, err = strconv.Atoi(arr[6])
	if err != nil {
		return
	}

	if p, ok := protocols[arr[7]]; ok {
		f.Protocol = p
	} else {
		f.Protocol = arr[7]
	}

	f.Packets, err = strconv.ParseFloat(arr[8], 64)
	if err != nil {
		return
	}
	f.Bytes, err = strconv.ParseFloat(arr[9], 64)
	if err != nil {
		return
	}

	unix, err := strconv.ParseInt(arr[10], 10, 64)
	if err != nil {
		return
	}
	f.Timestamp = time.Unix(unix, 0)

	if arr[12] == "ACCEPT" {
		f.Accepted = true
	}

	return f, nil
}

// FlowLogEvent is the structure of a flow log event from cloudwatch.
type FlowLogEvent struct {
	LogEvents []struct {
		ID        string `json:"id"`
		Message   string `json:"message"`
		Timestamp int    `json:"timestamp"`
	} `json:"logEvents"`
	LogGroup            string   `json:"logGroup"`
	LogStream           string   `json:"logStream"`
	MessageType         string   `json:"messageType"`
	Owner               string   `json:"owner"`
	SubscriptionFilters []string `json:"subscriptionFilters"`
}

// ProcessFlowLog parses a flowlog message and sets metrics.
func (f FlowLogEvent) ProcessFlowLog(denyLogChan chan<- FlowMessage) error {
	for i := range f.LogEvents {
		metrics.FlowEvents.Inc()

		msg, err := ParseMessage(f.LogEvents[i].Message)
		if err != nil {
			return err
		}

		if logDeny {
			go func() {
				denyLogChan <- msg
			}()
		}

		if srcNet, ok := subnets.Lookup(msg.Src); ok {

			metrics.SubnetPktsOut.WithLabelValues(srcNet).
				Add(msg.Packets)

			metrics.SubnetBytesOut.WithLabelValues(srcNet).
				Add(msg.Bytes)

		}

		if dstNet, ok := subnets.Lookup(msg.Dst); ok {
			metrics.SubnetPktsIn.WithLabelValues(dstNet).
				Add(msg.Packets)

			metrics.SubnetBytesIn.WithLabelValues(dstNet).
				Add(msg.Bytes)

			if msg.Accepted {

				metrics.SubnetAccepts.WithLabelValues(dstNet).
					Add(msg.Packets)

			} else {

				metrics.SubnetDenies.WithLabelValues(dstNet).
					Add(msg.Packets)

			}
		}
	}
	return nil
}

// Log denied flows.
func Log(denyLogChan <-chan FlowMessage) {

	if !logDeny {
		return
	}

	bulk := esClient.Bulk()

	for msg := range denyLogChan {

		if msg.Accepted {
			// Don't log accepted packets
			continue
		}

		if esClient == nil {
			// No ES server, just log to stdout
			log.Printf(
				"POLICY_DENIED: src:%s dst:%s srcport:%d dstport:%d proto:%s packets:%.0f timestamp:%s",
				msg.Src.String(),
				msg.Dst.String(),
				msg.SrcPort,
				msg.DstPort,
				msg.Protocol,
				msg.Packets,
				msg.Timestamp.String(),
			)
			continue
		}

		bulk = bulk.Add(
			elastic.NewBulkIndexRequest().
				Doc(msg).
				Type(esType).
				Index(esIndexName()),
		)

		if bulk.NumberOfActions() >= esBatchSize {
			exists, err := esClient.IndexExists(esIndexName()).Do()
			if err != nil {
				log.Fatal(err)
			}
			if !exists {
				log.Debug("Index is missing, creating index: ", esIndexName())
				_, err := esClient.CreateIndex(esIndexName()).Do()
				if err != nil {
					log.Fatal(err)
				}
				log.Debug("Created index")

				_, err = esClient.PutMapping().
					Index(esIndexName()).
					Type(esType).
					BodyString(MessageMapping).
					Do()
				if err != nil {
					log.Fatal(err)
				}
				log.Debug("Created mapping")
			}

			resp, err := bulk.Do()
			if err != nil {
				log.Error(err)
				continue
			}
			failedItems := resp.Failed()
			if len(failedItems) != 0 {
				log.Debugf("Failed to insert following items into Elasticsearch: %s", failedItems)
			}

		}

	}

}

// Metrics contains prometheus metrics.
type Metrics struct {
	SubnetPktsIn   *prometheus.CounterVec
	SubnetPktsOut  *prometheus.CounterVec
	SubnetBytesIn  *prometheus.CounterVec
	SubnetBytesOut *prometheus.CounterVec
	SubnetAccepts  *prometheus.CounterVec
	SubnetDenies   *prometheus.CounterVec
	FlowEvents     prometheus.Counter
}

// Register sets up the prometheus metrics.
func (m *Metrics) Register() {
	m.SubnetPktsIn = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flowlog_recieved_packets_total",
			Help: "The number of packets recieved on local subnets",
		},
		[]string{"subnet"},
	)

	m.SubnetPktsOut = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flowlog_sent_packets_total",
			Help: "The number of packets sent from local subnets",
		},
		[]string{"subnet"},
	)

	m.SubnetBytesIn = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flowlog_recieved_bytes_total",
			Help: "The number of bytes recieved on local subnets",
		},
		[]string{"subnet"},
	)

	m.SubnetBytesOut = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flowlog_sent_bytes_total",
			Help: "The number of bytes sent from local subnets",
		},
		[]string{"subnet"},
	)

	m.SubnetAccepts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flowlog_accepts_total",
			Help: "The number of packets accepted on local subnets",
		},
		[]string{"subnet"},
	)

	m.SubnetDenies = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flowlog_denies_total",
			Help: "The number of packets denied on local subnets",
		},
		[]string{"subnet"},
	)

	m.FlowEvents = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "flowlog_events_total",
			Help: "Total events processed",
		},
	)

	prometheus.MustRegister(m.SubnetPktsIn)
	prometheus.MustRegister(m.SubnetPktsOut)
	prometheus.MustRegister(m.SubnetBytesIn)
	prometheus.MustRegister(m.SubnetBytesOut)
	prometheus.MustRegister(m.SubnetAccepts)
	prometheus.MustRegister(m.SubnetDenies)
	prometheus.MustRegister(m.FlowEvents)
}

// Serve runs the metric webserver.
func (m *Metrics) Serve() {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	})

	log.Debug("Starting webserver")

	http.Handle("/metrics", prometheus.Handler())
	http.ListenAndServe(":"+port, nil)
}

func main() {

	// Run first get inline
	err := subnets.Get()
	if err != nil {
		log.Fatal(err)
	}

	if esURL != "" {
		// Connect to elasticsearch if es_url specified.
		log.Debugf("Connecting to Elasticsearch at %s", esURL)
		esClient, err = elastic.NewClient(
			elastic.SetURL(esURL),
			elastic.SetSniff(false),
		)
		if err != nil {
			log.Fatal(err)
		}
		log.Debug("Connected")
	}

	metrics.Register()

	// Periodically update subnet list
	go func() {
		for {
			time.Sleep(time.Minute * 30)
			err := subnets.Get()
			if err != nil {
				log.Error(err)
			}
		}
	}()

	gzipChan := make(chan []byte, getSize*int64(shards))
	decodeChan := make(chan io.Reader)
	logChan := make(chan FlowLogEvent)
	denyLogChan := make(chan FlowMessage, getSize*int64(shards))

	go Decode(decodeChan, logChan)
	go Decompress(gzipChan, decodeChan)

	for i := 0; i < shards; i++ {
		go Stream(i, gzipChan)
	}

	go metrics.Serve()

	go Log(denyLogChan)

	for msg := range logChan {
		err = msg.ProcessFlowLog(denyLogChan)
		if err != nil {
			log.Debug(err)
		}
	}

}
