package main

import (
	"context"
	"fmt"
	"time"

	_ "net/http/pprof"

	"github.com/lbryio/herald/internal"
	pb "github.com/lbryio/herald/protobuf/go"
	"github.com/lbryio/herald/server"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

func main() {

	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	ctx := context.Background()
	searchRequest := &pb.SearchRequest{}

	args := server.ParseArgs(searchRequest)

	if args.CmdType == server.ServeCmd {
		// This will cancel goroutines with the server finishes.
		ctxWCancel, cancel := context.WithCancel(ctx)
		defer cancel()

		initsignals()
		interrupt := interruptListener()

		s := server.MakeHubServer(ctxWCancel, args)
		go s.Run()

		defer func() {
			log.Println("Shutting down server...")

			s.EsClient.Stop()
			s.GrpcServer.GracefulStop()
			s.DB.Shutdown()

			log.Println("Returning from main...")
		}()

		<-interrupt
		return
	}

	conn, err := grpc.Dial("localhost:"+args.Port,
		grpc.WithInsecure(),
		grpc.WithBlock(),
	)
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	c := pb.NewHubClient(conn)

	ctxWTimeout, cancelQuery := context.WithTimeout(ctx, time.Second)
	defer cancelQuery()

	log.Println(args)
	switch args.CmdType {
	case server.SearchCmd:
		r, err := c.Search(ctxWTimeout, searchRequest)
		if err != nil {
			log.Fatal(err)
		}

		log.Printf("found %d results\n", r.GetTotal())

		for _, t := range r.Txos {
			fmt.Printf("%s:%d\n", internal.TxHashToTxId(t.TxHash), t.Nout)
		}
	default:
		log.Fatalln("Unknown Command Type!")
	}
}
