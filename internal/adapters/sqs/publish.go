package sqs

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
)

const MaxBatch = 10

var errUnacknowledged = errors.New("sqs did not acknowledge the entry")

func (c *Client) PublishEvents(ctx context.Context, msgs []app.OutboxMessage) []error {
	errs := make([]error, len(msgs))
	entries := make([]types.SendMessageBatchRequestEntry, len(msgs))
	for i, m := range msgs {
		entries[i] = types.SendMessageBatchRequestEntry{
			Id:                     aws.String(strconv.Itoa(i)),
			MessageBody:            aws.String(string(m.Payload)),
			MessageGroupId:         aws.String(m.PartitionKey.String()),
			MessageDeduplicationId: aws.String(m.ID.String()),
			MessageAttributes: map[string]types.MessageAttributeValue{
				"eventType":     {DataType: aws.String("String"), StringValue: aws.String(m.EventType)},
				"eventVersion":  {DataType: aws.String("Number"), StringValue: aws.String(strconv.Itoa(m.EventVersion))},
				"correlationId": {DataType: aws.String("String"), StringValue: aws.String(m.CorrelationID)},
			},
		}
	}
	out, err := c.API.SendMessageBatch(ctx, &awssqs.SendMessageBatchInput{QueueUrl: aws.String(c.urls.Events), Entries: entries})
	if err != nil {
		for i := range errs {
			errs[i] = err
		}
		return errs
	}
	for i := range errs {
		errs[i] = errUnacknowledged
	}
	for _, ok := range out.Successful {
		if i, valid := entry(aws.ToString(ok.Id), len(errs)); valid {
			errs[i] = nil
		}
	}
	for _, f := range out.Failed {
		if i, valid := entry(aws.ToString(f.Id), len(errs)); valid {
			errs[i] = fmt.Errorf("sqs rejected the entry: %s %s", aws.ToString(f.Code), aws.ToString(f.Message))
		}
	}
	return errs
}

func entry(id string, n int) (int, bool) {
	i, err := strconv.Atoi(id)
	return i, err == nil && i >= 0 && i < n
}
