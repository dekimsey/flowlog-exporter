# Flowlog exporter

This exports Amazon EC2 flowlogs to prometheus.

## What does this achieve?

Processing the EC2 flowlogs means you can monitor the traffic entering and leaving your subnets, and monitor accepted and deined traffic.
This also gives you a (very) simplistic IDS capability, as you can track spikes in rejected flows as potentially suspicious network activity.

## What do I need to do to make this work?

First enable flow logs for your VPC(s), as described here:
http://docs.aws.amazon.com/AmazonVPC/latest/UserGuide/flow-logs.html

Secondly, create a kinesis stream, and stream your logs to it, as described here:
http://docs.aws.amazon.com/AmazonCloudWatch/latest/DeveloperGuide/Subscriptions.html

Now create an IAM keypair that can read the kinesis stream and has permissions to list all subnets in your VPC(s).

Once done, and equipped with an AWS key pair above, simply start the exporter.
AWS credentials should be provided in the environment variables ```AWS_ACCESS_KEY``` and ```AWS_SECRET_KEY```.
Set the arguments ```-stream``` and ```-shards``` to match your kinesis configuration.

## Limitations

Because this uses labels to expose data, data aggregation is done on a per-subnet level (having a label for each IP would place excessive load on the server).
Currently, the implementation only reports data where the source or destination is a local subnet.  The list of subnets is refreshed every 30 minutes.