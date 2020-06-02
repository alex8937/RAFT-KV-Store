package main

import (
	"encoding/json"
	"fmt"
	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/raft-kv-store/client"
	"github.com/raft-kv-store/common"
	"github.com/raft-kv-store/config"
	"github.com/raft-kv-store/coordinator"
	httpd "github.com/raft-kv-store/http"
	"github.com/raft-kv-store/raftpb"
	"github.com/raft-kv-store/store"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	clusterConfigTestFilePath = "test/cluster-config-test.json"
	testBucketName            = "bucketName-test/shard"
)

type Role struct {
	Node   string `json:"node"`
	Listen string `json:"listen"`
	Raft   string `json:"raft"`
	Join   string `json:"join"`
}

type Cluster struct {
	Coordinators []Role   `json:"coordinators"`
	Shards       [][]Role `json:"shards"`
}

func readClusterConfig(filePath string) (*Cluster, error) {
	var clusterConfig Cluster
	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &clusterConfig); err != nil {
		return nil, err
	}
	return &clusterConfig, nil
}

func getShardsConfig(c *Cluster) *config.ShardsConfig {
	shardInfo := &config.ShardsConfig{}
	for _, shards := range c.Shards {
		var shard []string
		for _, s := range shards {
			shard = append(shard, s.Listen)
		}
		shardInfo.Shards = append(shardInfo.Shards, shard)
	}
	return shardInfo
}

func setupCluster(filePath string) error {
	var wg sync.WaitGroup
	logger := log.New()
	logger.SetFormatter(&nested.Formatter{
		HideKeys:    true,
		FieldsOrder: []string{"component"},
		CustomCallerFormatter: func(f *runtime.Frame) string {
			s := strings.Split(f.Function, ".")
			funcName := s[len(s)-1]
			return fmt.Sprintf(" [%s:%d][%s()]", path.Base(f.File), f.Line, funcName)
		},
		CallerFirst: true,
	})
	logger.SetReportCaller(true)

	clusterConfig, err := readClusterConfig(filePath)
	if err != nil {
		log.Fatal(err)
	}
	shardsConfig := getShardsConfig(clusterConfig)
	for _, c := range clusterConfig.Coordinators {
		wg.Add(1)
		go func(c Role) {
			defer wg.Done()
			if c.Join != "" {
				time.Sleep(5 * time.Second)
			}
			co := coordinator.NewCoordinator(logger, c.Node, "", c.Raft, c.Join == "", shardsConfig)
			h := httpd.NewService(logger, c.Listen, co)
			h.Start(c.Join)
		}(c)
	}

	wg.Wait()
	for _, shards := range clusterConfig.Shards {
		for _, s := range shards {
			wg.Add(1)
			go func(s Role) {
				defer wg.Done()
				if s.Join != "" {
					time.Sleep(5 * time.Second)
				}
				kv := store.NewStore(logger, s.Node, s.Raft, "", s.Join == "", s.Listen, testBucketName)
				kv.Start(s.Join, s.Node)
			}(s)
		}
	}
	wg.Wait()
	log.Println("Cluster finished")
	return nil
}

func TestMultiClients() {
	var clients []*client.RaftKVClient
	for i := 0; i < 10; i++ {
		clients = append(clients, client.NewRaftKVClient(":41000"))
		clients[i].TxnCmds = &raftpb.RaftCommand{
			Commands: []*raftpb.Command{
				{Method: common.SET, Key: "test3", Value: int64(i)},
				{Method: common.SET, Key: "test4", Value: int64(-i)},
			},
			IsTxn: true,
		}
	}
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *client.RaftKVClient) {
			defer wg.Done()
			res, err := c.Transaction()
			fmt.Println(res, err, c.TxnCmds)
		}(c)
	}
	wg.Wait()
	wg.Add(1)
	go func(c *client.RaftKVClient) {
		defer wg.Done()
		fmt.Println(c.Get("test3"))
	}(clients[0])

	wg.Wait()
}

func main() {
	common.SnapshotInterval = 30
	common.SnapshotThreshold = 5
	setupCluster(clusterConfigTestFilePath)

		TestMultiClients()

	time.Sleep(60 * time.Second)

}
