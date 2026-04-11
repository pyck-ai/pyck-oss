#!/bin/sh
set -e

# Load environment variables from .env and export them
eval "$(sed 's/^/export /' < .env.example)" > /dev/null 2>&1

export AWS_ACCESS_KEY_ID="$PYCK_AWS_ACCESS_KEY_ID"
export AWS_SECRET_ACCESS_KEY="$PYCK_AWS_SECRET_ACCESS_KEY"
export AWS_DEFAULT_REGION="$PYCK_AWS_S3_REGION"
export AWS_ENDPOINT_URL="$PYCK_AWS_S3_HTTP_ENDPOINT_URL"

# Check if the bucket exists
if aws s3api head-bucket 2>/dev/null \
  --bucket "$PYCK_AWS_S3_BUCKET"
then
  echo "Bucket '$PYCK_AWS_S3_BUCKET' already exists."
else
  echo "Creating bucket '$PYCK_AWS_S3_BUCKET'..."
  aws s3 mb "s3://$PYCK_AWS_S3_BUCKET"
fi
