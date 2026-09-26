#!/bin/sh
set -eu

queue_url() {
    aws sqs get-queue-url --queue-name "$1" --query QueueUrl --output text 2>/dev/null ||
        aws sqs create-queue --queue-name "$1" --attributes FifoQueue=true --query QueueUrl --output text
}

queue_arn() {
    aws sqs get-queue-attributes --queue-url "$1" --attribute-names QueueArn --query Attributes.QueueArn --output text
}

redrive_policy() {
    printf '{\\"deadLetterTargetArn\\":\\"%s\\",\\"maxReceiveCount\\":\\"%s\\"}' "$1" "$2"
}

fifo_queue() {
    url=$(queue_url "$1")
    aws sqs set-queue-attributes --queue-url "$url" --attributes "$2"
    echo "$url"
}

user_policy() {
    aws iam get-user --user-name "$1" >/dev/null 2>&1 || aws iam create-user --user-name "$1" >/dev/null
    aws iam put-user-policy --user-name "$1" --policy-name "$1" --policy-document "$2"
}

base='"ContentBasedDeduplication":"false","VisibilityTimeout":"30","MessageRetentionPeriod":"1209600"'

input_dlq=$(fifo_queue wager-transactions-dlq.fifo "{$base}")
events_dlq=$(fifo_queue wallet-events-dlq.fifo "{$base}")
input=$(fifo_queue wager-transactions.fifo \
    "{$base,\"ReceiveMessageWaitTimeSeconds\":\"20\",\"RedrivePolicy\":\"$(redrive_policy "$(queue_arn "$input_dlq")" 10)\"}")
events=$(fifo_queue wallet-events.fifo \
    "{$base,\"RedrivePolicy\":\"$(redrive_policy "$(queue_arn "$events_dlq")" 10)\"}")

input_arn=$(queue_arn "$input")
input_dlq_arn=$(queue_arn "$input_dlq")
events_arn=$(queue_arn "$events")

user_policy provider-producer "$(printf '{"Version":"2012-10-17","Statement":[
{"Effect":"Allow","Action":["sqs:SendMessage","sqs:GetQueueUrl"],"Resource":"%s"}]}' "$input_arn")"

user_policy wallet-service "$(printf '{"Version":"2012-10-17","Statement":[
{"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:ChangeMessageVisibility","sqs:GetQueueAttributes","sqs:GetQueueUrl"],"Resource":"%s"},
{"Effect":"Allow","Action":["sqs:SendMessage","sqs:GetQueueUrl"],"Resource":"%s"},
{"Effect":"Allow","Action":["sqs:SendMessage","sqs:GetQueueAttributes","sqs:GetQueueUrl"],"Resource":"%s"}]}' \
    "$input_arn" "$input_dlq_arn" "$events_arn")"

user_policy events-reader "$(printf '{"Version":"2012-10-17","Statement":[
{"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:ChangeMessageVisibility","sqs:GetQueueAttributes","sqs:GetQueueUrl"],"Resource":"%s"}]}' \
    "$events_arn")"

printf '%s\n' "$input" "$input_dlq" "$events" "$events_dlq"
