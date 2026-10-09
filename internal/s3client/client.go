// Package s3client membungkus AWS SDK Go v2 untuk kebutuhan tafw:
// PutObject (upload input). FASE 3 akan menambah ListObjectsV2 dan GetObject.
package s3client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"tafw/internal/config"
)

// Client membungkus klien S3 dan uploader (multipart, aman untuk file besar).
type Client struct {
	s3       *s3.Client
	uploader *manager.Uploader
	cfg      *config.Config
}

// New membuat Client sesuai konfigurasi. Kredensial diambil dari file (mode
// "file") atau dari default chain (mode "default-chain", siap IAM Roles Anywhere).
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.S3.Region),
	}

	if cfg.Credentials.Mode == "file" {
		c, err := LoadCredentials(cfg.Credentials.File)
		if err != nil {
			return nil, err
		}
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, c.SessionToken),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("muat konfigurasi AWS: %w", err)
	}

	var s3Opts []func(*s3.Options)
	if cfg.S3.Endpoint != "" {
		endpoint := cfg.S3.Endpoint
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
		})
	}

	cli := s3.NewFromConfig(awsCfg, s3Opts...)
	return &Client{
		s3:       cli,
		uploader: manager.NewUploader(cli),
		cfg:      cfg,
	}, nil
}

// PutFile mengunggah file lokal ke key S3, memakai SSE bila dikonfigurasi.
// File besar otomatis diunggah multipart oleh manager.Uploader.
func (c *Client) PutFile(ctx context.Context, localPath, key string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("buka file %s: %w", localPath, err)
	}
	defer f.Close()

	in := &s3.PutObjectInput{
		Bucket: aws.String(c.cfg.S3.Bucket),
		Key:    aws.String(key),
		Body:   f,
	}

	switch c.cfg.S3.SSE {
	case "AES256":
		in.ServerSideEncryption = types.ServerSideEncryptionAes256
	case "aws:kms":
		in.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		if c.cfg.S3.KMSKeyID != "" {
			in.SSEKMSKeyId = aws.String(c.cfg.S3.KMSKeyID)
		}
	}

	if _, err := c.uploader.Upload(ctx, in); err != nil {
		return fmt.Errorf("upload %s ke s3://%s/%s: %w", localPath, c.cfg.S3.Bucket, key, err)
	}
	return nil
}

// ObjectInfo adalah metadata ringkas sebuah objek S3.
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
}

// List mengembalikan semua objek di bawah prefix (rekursif, dengan paginasi).
func (c *Client) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	p := s3.NewListObjectsV2Paginator(c.s3, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.cfg.S3.Bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list s3://%s/%s: %w", c.cfg.S3.Bucket, prefix, err)
		}
		for _, o := range page.Contents {
			info := ObjectInfo{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size)}
			if o.LastModified != nil {
				info.LastModified = *o.LastModified
			}
			if o.ETag != nil {
				info.ETag = aws.ToString(o.ETag)
			}
			out = append(out, info)
		}
	}
	return out, nil
}

// GetStream membuka objek S3 sebagai stream. Pemanggil wajib menutupnya.
func (c *Client) GetStream(ctx context.Context, key string) (io.ReadCloser, error) {
	res, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.cfg.S3.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("get s3://%s/%s: %w", c.cfg.S3.Bucket, key, err)
	}
	return res.Body, nil
}

// GetBytes mengunduh seluruh isi objek kecil (mis. status.json) ke memori.
func (c *Client) GetBytes(ctx context.Context, key string) ([]byte, error) {
	rc, err := c.GetStream(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Head melaporkan apakah objek ada (true) atau tidak (false, tanpa error).
func (c *Client) Head(ctx context.Context, key string) (bool, error) {
	_, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.cfg.S3.Bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return false, nil
	}
	// Sebagian backend mengembalikan NoSuchKey.
	if strings.Contains(err.Error(), "NoSuchKey") || strings.Contains(err.Error(), "NotFound") {
		return false, nil
	}
	return false, err
}

// Credentials adalah pasangan access key / secret key dari file 0600.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// LoadCredentials membaca file kredensial sederhana berformat KEY=VALUE
// (mendukung komentar dengan '#'). File harus dimiliki root dengan permission
// ketat (0600/0640) - lihat README.
//
// Contoh isi:
//
//	AWS_ACCESS_KEY_ID=AKIA...
//	AWS_SECRET_ACCESS_KEY=...
//	AWS_SESSION_TOKEN=...   # opsional
func LoadCredentials(path string) (Credentials, error) {
	f, err := os.Open(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("buka file kredensial: %w", err)
	}
	defer f.Close()

	var c Credentials
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "AWS_ACCESS_KEY_ID", "access_key_id":
			c.AccessKeyID = val
		case "AWS_SECRET_ACCESS_KEY", "secret_access_key":
			c.SecretAccessKey = val
		case "AWS_SESSION_TOKEN", "session_token":
			c.SessionToken = val
		}
	}
	if err := sc.Err(); err != nil {
		return Credentials{}, fmt.Errorf("baca file kredensial: %w", err)
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return Credentials{}, fmt.Errorf("file kredensial %s tidak memuat AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY", path)
	}
	return c, nil
}
