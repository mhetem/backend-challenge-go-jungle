package sqstest

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/mhetem/backend-challenge-go-jungle/test/integration/dbtest"
)

type Queues struct {
	t        *testing.T
	API      *awssqs.Client
	Input    string
	InputURL string
	DLQ      string
	DLQURL   string
}

type Message struct {
	Body       string
	GroupID    string
	Attributes map[string]string
}

func New(t *testing.T, maxReceiveCount int) *Queues {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(dbtest.Env(t, "AWS_REGION")),
		awsconfig.WithBaseEndpoint(dbtest.Env(t, "AWS_ENDPOINT_URL")))
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	suffix := uuid.NewString()[:8]
	q := &Queues{t: t, API: awssqs.NewFromConfig(cfg), Input: "input-" + suffix + ".fifo", DLQ: "input-dlq-" + suffix + ".fifo"}
	q.DLQURL = q.create(q.DLQ, nil)
	arn := q.attribute(q.DLQURL, types.QueueAttributeNameQueueArn)
	q.InputURL = q.create(q.Input, map[string]string{
		"RedrivePolicy": fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":"%d"}`, arn, maxReceiveCount),
	})
	return q
}

func (q *Queues) create(name string, extra map[string]string) string {
	q.t.Helper()
	attrs := map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}
	maps.Copy(attrs, extra)
	out, err := q.API.CreateQueue(context.Background(), &awssqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attrs})
	if err != nil {
		q.t.Fatalf("create queue %s: %v", name, err)
	}
	url := aws.ToString(out.QueueUrl)
	q.t.Cleanup(func() {
		_, _ = q.API.DeleteQueue(context.Background(), &awssqs.DeleteQueueInput{QueueUrl: aws.String(url)})
	})
	return url
}

func (q *Queues) attribute(url string, name types.QueueAttributeName) string {
	q.t.Helper()
	out, err := q.API.GetQueueAttributes(context.Background(), &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []types.QueueAttributeName{name},
	})
	if err != nil {
		q.t.Fatalf("queue attribute %s: %v", name, err)
	}
	return out.Attributes[string(name)]
}

func (q *Queues) Send(body, group, dedup string) {
	q.t.Helper()
	_, err := q.API.SendMessage(context.Background(), &awssqs.SendMessageInput{
		QueueUrl:               aws.String(q.InputURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(group),
		MessageDeduplicationId: aws.String(dedup),
	})
	if err != nil {
		q.t.Fatalf("send %s: %v", dedup, err)
	}
}

func (q *Queues) Counts(url string) (visible, inFlight int) {
	q.t.Helper()
	visible, _ = strconv.Atoi(q.attribute(url, types.QueueAttributeNameApproximateNumberOfMessages))
	inFlight, _ = strconv.Atoi(q.attribute(url, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible))
	return visible, inFlight
}

func (q *Queues) Drain(url string, want int, wait time.Duration) []Message {
	q.t.Helper()
	var got []Message
	deadline := time.Now().Add(wait)
	for len(got) < want && time.Now().Before(deadline) {
		out, err := q.API.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(url),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameMessageGroupId},
			MessageAttributeNames:       []string{"All"},
		})
		if err != nil {
			q.t.Fatalf("receive: %v", err)
		}
		for _, m := range out.Messages {
			attrs := map[string]string{}
			for k, v := range m.MessageAttributes {
				attrs[k] = aws.ToString(v.StringValue)
			}
			got = append(got, Message{
				Body:       aws.ToString(m.Body),
				GroupID:    m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
				Attributes: attrs,
			})
			if _, err := q.API.DeleteMessage(context.Background(), &awssqs.DeleteMessageInput{QueueUrl: aws.String(url), ReceiptHandle: m.ReceiptHandle}); err != nil {
				q.t.Fatalf("delete: %v", err)
			}
		}
	}
	return got
}
