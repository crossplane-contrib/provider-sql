package mysql

import (
	"context"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/awsiam"
)

// ApplyIAM injects a fresh RDS IAM token when source is AWSIAMAuth and reports
// that the MySQL client must allow the cleartext plugin. Any other source is
// left unchanged and cleartext stays off, so a password login keeps its DSN.
func ApplyIAM(ctx context.Context, source string, region *string, creds map[string][]byte) (bool, error) {
	if source != awsiam.SourceAWSIAMAuth {
		return false, nil
	}
	if err := awsiam.InjectIAM(ctx, region, creds); err != nil {
		return false, err
	}
	return true, nil
}
