package sqs

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/lifecycle"
)

var Module = fx.Module("sqs",
	fx.Provide(
		New,
		fx.Annotate(check, fx.ResultTags(`group:"health.checks"`)),
	),
	fx.Invoke(func(lc fx.Lifecycle, c *Client) {
		lc.Append(fx.Hook{OnStart: c.Start, OnStop: c.Stop})
	}),
)

type Queues struct {
	Input    string
	InputDLQ string
	Events   string
}

type Client struct {
	API       *awssqs.Client
	names     Queues
	urls      Queues
	transport *http.Transport
	log       *slog.Logger
}

func New(cfg config.Config, log *slog.Logger) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.AWS.Region),
		awsconfig.WithHTTPClient(&http.Client{Transport: transport}),
	}
	if cfg.AWS.EndpointURL != "" {
		opts = append(opts, awsconfig.WithBaseEndpoint(cfg.AWS.EndpointURL))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, err
	}
	return &Client{
		API:       awssqs.NewFromConfig(awsCfg),
		names:     Queues{Input: cfg.SQS.InputQueue, InputDLQ: cfg.SQS.InputDLQ, Events: cfg.SQS.EventsQueue},
		transport: transport,
		log:       log,
	}, nil
}

func (c *Client) Start(ctx context.Context) error {
	return lifecycle.Retry(ctx, c.log, "sqs", c.resolve)
}

func (c *Client) Stop(context.Context) error {
	c.transport.CloseIdleConnections()
	return nil
}

func (c *Client) Queues() Queues {
	return c.urls
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.API.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(c.urls.Input),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	return err
}

func (c *Client) resolve(ctx context.Context) error {
	var urls Queues
	queues := []struct {
		name string
		url  *string
	}{
		{c.names.Input, &urls.Input},
		{c.names.InputDLQ, &urls.InputDLQ},
		{c.names.Events, &urls.Events},
	}
	for _, q := range queues {
		out, err := c.API.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(q.name)})
		if err != nil {
			return fmt.Errorf("queue %s: %w", q.name, err)
		}
		*q.url = aws.ToString(out.QueueUrl)
	}
	c.urls = urls
	return nil
}

func check(c *Client) health.Check {
	return health.Check{Name: "sqs", Probe: c.Ping}
}
