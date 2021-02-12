package main

import (
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"
)

type DynamoDBMock struct {
	dynamodbiface.DynamoDBAPI
	maxHeartbeats uint64
	heartbeats map[string]uint64
	mu sync.Mutex
}

func NewDynamoDBMock(iface dynamodbiface.DynamoDBAPI, maxHeartbeats uint64) *DynamoDBMock {
	return &DynamoDBMock{
		DynamoDBAPI: iface,
		maxHeartbeats: maxHeartbeats,
		heartbeats: make(map[string]uint64),
	}
}

func (ddb *DynamoDBMock) UpdateItemWithContext(ctx aws.Context, input *dynamodb.UpdateItemInput, options ...request.Option) (*dynamodb.UpdateItemOutput, error) {
	if *input.ExpressionAttributeNames["#3"] == "leaseDuration" {
		owner := *input.ExpressionAttributeValues[":1"].S
		ddb.mu.Lock()
		ddb.heartbeats[owner] += 1
		hb := ddb.heartbeats[owner]
		ddb.mu.Unlock()

		if hb > ddb.maxHeartbeats {
			fmt.Printf("[%s]: stopped heartbeat\n", owner)
			return nil, errors.New("disconnected from AWS")
		} else {
			fmt.Printf("[%s]: %d\n", owner, hb)
		}
	}

	return ddb.DynamoDBAPI.UpdateItemWithContext(ctx, input, options...)
}
