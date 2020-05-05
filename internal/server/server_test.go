package server

import (
	"context"
	"io/ioutil"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	api "github.com/tetronoz/proglog/api/v1"
	"github.com/tetronoz/proglog/internal/log"
	"google.golang.org/grpc"
)

func TestServer(t *testing.T) {
	for scenario, fn := range map[string]func(t *testing.T, client api.LogClient, config *Config){
		"produce/consume a message to/from the log succeeeds": testProduceConsume,
		"consume past log boundary fails":                     testConsumePastBoundary,
		"produce/consume stream succeeds":                     testProduceConsumeStream,
	} {
		t.Run(scenario, func(t *testing.T) {
			client, config, teardown := testSetup(t, nil)
			defer teardown()
			fn(t, client, config)
		})
	}
}

func testSetup(t *testing.T, fn func(*Config)) (client api.LogClient, config *Config, teardown func()) {
	t.Helper()
	
	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	
	clientOptions := []grpc.DialOption{grpc.WithInsecure()}
	cc, err := grpc.Dial(l.Addr().String(), clientOptions...)
	require.NoError(t, err)
	
	dir, err := ioutil.TempDir("", "server-test")
	require.NoError(t, err)
	
	clog, err := log.NewLog(dir, log.Config{})
	require.NoError(t, err)
	
	config = &Config{
		CommitLog: clog,
	}
	
	if fn != nil {
		fn(config)
	}
	server, err := NewGRPCServer(config)
	require.NoError(t, err)
	go func() {
		server.Serve(l)
	}()
	
	client = api.NewLogClient(cc)
	
	return client, config, func() {
		server.Stop()
		cc.Close()
		l.Close()
	}
}

func testConsumeEmpty(t *testing.T, client api.LogClient, config *Config) {
	consume, err := client.Consume(context.Background(), &api.ConsumeRequest{
		Offset: 0,
	})
	require.Nil(t, consume)
	require.Equal(t, grpc.Code(err), grpc.Code(api.ErrOffsetOutOfRange{}.GRPCStatus().Err()))
}

func testProduceConsume(t *testing.T, client api.LogClient, config *Config) {
	ctx := context.Background()

	want := &api.Record{
		Value: []byte("hello world"),
	}

	produce, err := client.Produce(context.Background(), &api.ProduceRequest{
		Record: want,
	})
	require.NoError(t, err)

	consume, err := client.Consume(ctx, &api.ConsumeRequest{
		Offset: produce.Offset,
	})
	require.NoError(t, err)
	require.Equal(t, want, consume.Record)
}

func testConsumePastBoundary(t *testing.T, client api.LogClient, config *Config) {
	ctx := context.Background()

	produce, err := client.Produce(ctx, &api.ProduceRequest{
		Record: &api.Record{
			Value: []byte("hello world"),
		},
	})

	require.NoError(t, err)

	consume, err := client.Consume(ctx, &api.ConsumeRequest{
		Offset: produce.Offset + 1,
	})
	if consume != nil {
		t.Fatal("consume not nil")
	}
	got, want := grpc.Code(err), grpc.Code(api.ErrOffsetOutOfRange{}.GRPCStatus().Err())
	if got != want {
		t.Fatalf("got err: %v, want: %v", got, want)
	}
}

func testProduceConsumeStream(t *testing.T, client api.LogClient, config *Config) {
	ctx := context.Background()

	records := []*api.Record{{
		Value: []byte("first message"),
	}, {
		Value: []byte("second message"),
	}}

	{
		stream, err := client.ProduceStream(ctx)
		require.NoError(t, err)

		for offset, record := range records {
			err = stream.Send(&api.ProduceRequest{
				Record: record,
			})
			require.NoError(t, err)
			res, err := stream.Recv()
			require.NoError(t, err)
			if res.Offset != uint64(offset) {
				t.Fatalf("got offset: %d, want: %d", res.Offset, offset)
			}
		}
	}

	{
		stream, err := client.ConsumeStream(ctx, &api.ConsumeRequest{Offset: 0})
		require.NoError(t, err)

		for _, record := range records {
			res, err := stream.Recv()
			require.NoError(t, err)
			require.Equal(t, res.Record, record)
		}
	}
}