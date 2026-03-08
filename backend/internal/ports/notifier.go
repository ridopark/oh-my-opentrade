package ports

import (
	"context"
)

type Attachment struct {
	Data     []byte
	Filename string
}

type NotifierPort interface {
	Notify(ctx context.Context, tenantID string, message string) error
}

type ImageNotifierPort interface {
	NotifierPort
	NotifyWithImage(ctx context.Context, tenantID, message string, image Attachment) error
}
