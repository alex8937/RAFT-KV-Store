package coordinator

import (
	"errors"
	"fmt"
	"net/rpc"

	"github.com/raft-kv-store/common"
	"github.com/raft-kv-store/raftpb"
)

const maxFindLeaderRetries = 5

// GetShardID return mapping from key to shardID
func (c *Coordinator) GetShardID(key string) int64 {
	h := 0
	nshards := len(c.ShardToPeers)
	for _, c := range key {
		h = 31*h + int(c)
	}
	return int64(h % nshards)
}

// Leader returns rpc address if the cohort is leader, otherwise return empty string
func (c *Coordinator) Leader(address string) (string, error) {
	var response raftpb.RPCResponse
	cmd := &raftpb.RaftCommand{
		Commands: []*raftpb.Command{
			{
				Method: common.LEADER,
			},
		},
	}

	client, err := rpc.DialHTTP("tcp", address)
	if err != nil {
		return "", err
	}

	err = client.Call("Cohort.ProcessCommands", cmd, &response)
	return response.Addr, err
}

func (c *Coordinator) updateLeaderTable(key string) (string, error) {
	// make rpc calls to get the leader
	shardID := c.GetShardID(key)
	nodes := c.ShardToPeers[shardID]
	for _, nodeAddr := range nodes {
		leader, err := c.Leader(nodeAddr)
		if err == nil && leader != "" {
			c.log.Infof("Update leader table: Shard %d -> %s", shardID, leader)
			c.shardLeaderTable[shardID] = leader
			return leader, nil
		}
	}
	return "", fmt.Errorf("shard %d is not reachable", shardID)
}

func (c *Coordinator) findLeaderFromCache(key string) (addr string, err error) {
	shardID := c.GetShardID(key)
	addr, ok := c.shardLeaderTable[shardID]
	if !ok {
		addr, err := c.updateLeaderTable(key)
		return addr, err
	}
	c.log.Infof("Fetch leader table: Shard %d -> %s", shardID, addr)
	return addr, nil
}

// SendMessageToShard sends prepare message to a shard. The return value
// indicates if the shard successfully performed the operation.
func (c *Coordinator) SendMessageToShard(ops *raftpb.ShardOps) ([]*raftpb.Command, error) {
	var response raftpb.RPCResponse

	// read leader from cache
	addr, err := c.findLeaderFromCache(ops.MasterKey)
	if err != nil {
		return nil, err
	}
	var client *rpc.Client
	var retries int
	for retries < maxFindLeaderRetries {
		client, err = rpc.DialHTTP("tcp", addr)
		if err != nil {
			return nil, err
		}
		err = client.Call("Cohort.ProcessTransactionMessages", ops, &response)
		// if not non-leader error, break
		if response.Phase != common.NotLeader {
			break
		}
		retries++
		// otherwise update the table
		addr, err = c.updateLeaderTable(ops.MasterKey)
		if err != nil {
			return nil, err
		}
	}

	success := response.Phase == (common.Prepared) || response.Phase == (common.Committed) || response.Phase == (common.Aborted)
	if success {
		return response.Commands, nil
	}
	return response.Commands, errors.New(response.Phase)
}
