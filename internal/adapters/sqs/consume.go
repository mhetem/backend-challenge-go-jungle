package sqs

import (
	"context"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

var traceAttributes = []string{"traceparent", "tracestate"}

type Message struct {
	ID            string
	Body          string
	ReceiptHandle string
	GroupID       string
	ReceiveCount  int
	Attributes    map[string]string
}

func (c *Client) Receive(ctx context.Context, wait, visibility time.Duration) ([]Message, error) {
	out, err := c.API.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.urls.Input),
		MaxNumberOfMessages: MaxBatch,
		WaitTimeSeconds:     int32(wait / time.Second),
		VisibilityTimeout:   int32(visibility / time.Second),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
			types.MessageSystemAttributeNameMessageGroupId,
		},
		MessageAttributeNames: traceAttributes,
	})
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, len(out.Messages))
	for i, m := range out.Messages {
		count, _ := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
		attrs := map[string]string{}
		for name, v := range m.MessageAttributes {
			if v.StringValue != nil {
				attrs[name] = *v.StringValue
			}
		}
		msgs[i] = Message{
			ID:            aws.ToString(m.MessageId),
			Body:          aws.ToString(m.Body),
			ReceiptHandle: aws.ToString(m.ReceiptHandle),
			GroupID:       m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
			ReceiveCount:  max(count, 1),
			Attributes:    attrs,
		}
	}
	return msgs, nil
}

func (c *Client) Delete(ctx context.Context, m Message) error {
	_, err := c.API.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.urls.Input),
		ReceiptHandle: aws.String(m.ReceiptHandle),
	})
	return err
}

func (c *Client) ChangeVisibility(ctx context.Context, m Message, d time.Duration) error {
	_, err := c.API.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.urls.Input),
		ReceiptHandle:     aws.String(m.ReceiptHandle),
		VisibilityTimeout: int32(d / time.Second),
	})
	return err
}

func (c *Client) DeadLetter(ctx context.Context, m Message, reason, detail string) error {
	attrs := map[string]types.MessageAttributeValue{
		"failureReason": {DataType: aws.String("String"), StringValue: aws.String(reason)},
		"failureDetail": {DataType: aws.String("String"), StringValue: aws.String(detail)},
	}
	for _, name := range traceAttributes {
		if v := m.Attributes[name]; v != "" {
			attrs[name] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
		}
	}
	_, err := c.API.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(c.urls.InputDLQ),
		MessageBody:            aws.String(m.Body),
		MessageGroupId:         aws.String(m.GroupID),
		MessageDeduplicationId: aws.String(m.ID),
		MessageAttributes:      attrs,
	})
	return err
}
