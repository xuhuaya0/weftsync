package feishu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rclone/rclone/fs"
)

// Chunked (block) upload for files above the 20MB single-request cap.
//
// Feishu's protocol is three calls: upload_prepare (returns upload_id,
// block_size, block_num), upload_part once per block, upload_finish (returns the
// file_token). The official docs state all three "does not support concurrent
// calls" and are capped at 5 QPS / 10000 per day, so blocks are sent strictly
// one at a time. That is why this backend deliberately does not implement
// fs.OpenChunkWriter: rclone would only gain concurrency we are forbidden to
// use, while the code would have to handle out-of-order commits.
//
// Memory stays at one block (the server's block_size, 4MB today) rather than the
// whole file, so a 1GB upload does not need 1GB of RAM.

// putChunked streams in to the remote using the block protocol.
//
// size must be the exact number of bytes available from in; the caller
// guarantees it is known and greater than MaxDirectUpload.
func (f *Fs) putChunked(ctx context.Context, in io.Reader, remote, name, parentToken string, size int64) (string, error) {
	uploadID, blockSize, serverBlockNum, err := f.client.uploadPrepare(ctx, parentToken, name, size)
	if err != nil {
		return "", err
	}

	// We slice the data, so our own arithmetic has to agree with the server's.
	// A disagreement means the contract changed; guessing would produce a
	// "block missing" failure after uploading everything.
	wantNum := int((size + blockSize - 1) / blockSize)
	if serverBlockNum != wantNum {
		return "", fmt.Errorf(
			"feishu: upload_prepare for %q returned block_num=%d with block_size=%d, "+
				"but %d bytes split into blocks of that size needs %d blocks. "+
				"Refusing to guess which number is right",
			remote, serverBlockNum, blockSize, size, wantNum)
	}

	fs.Debugf(f, "uploading %q in %d block(s) of %d bytes (upload_id %s)",
		remote, wantNum, blockSize, uploadID)

	buf := make([]byte, blockSize)
	for seq := 0; seq < wantNum; seq++ {
		// The last block is short unless the size is an exact multiple.
		want := blockSize
		if remaining := size - int64(seq)*blockSize; remaining < want {
			want = remaining
		}

		chunk := buf[:want]
		n, err := io.ReadFull(in, chunk)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("feishu: read block %d of %q: %w", seq, remote, err)
		}
		if int64(n) != want {
			return "", fmt.Errorf(
				"feishu: %q shrank while uploading: block %d needed %d bytes but the "+
					"source ended after %d. Nothing was committed to Feishu",
				remote, seq, want, n)
		}

		if err := f.uploadPartRetry(ctx, remote, uploadID, seq, wantNum, chunk); err != nil {
			return "", err
		}
	}

	// A source longer than the declared size would silently upload a truncated
	// file, so check that we consumed exactly what upload_prepare announced.
	var extra [1]byte
	if n, _ := in.Read(extra[:]); n > 0 {
		return "", fmt.Errorf(
			"feishu: %q grew while uploading: upload_prepare declared %d bytes but the "+
				"source has more. The uploaded blocks were discarded",
			remote, size)
	}

	return f.client.uploadFinish(ctx, uploadID, wantNum)
}

// Block-level retry budget, on top of the one inside client.do.
//
// client.do already retries a failed request three times, which covers a blip.
// What it does not cover is a longer interruption - a laptop changing networks,
// a VPN reconnecting - while a 1GB upload is at block 200 of 250. Without this
// layer the only recovery is rclone restarting the whole file, i.e. re-sending
// everything. Retrying the block is strictly cheaper and cannot produce a
// different result: upload_part for a given seq either lands or does not.
const blockRetries = 4

// blockRetryBaseWait is a variable so a test can compress the backoff instead of
// spending 30 seconds proving that it waits.
var blockRetryBaseWait = 2 * time.Second

// uploadPartRetry sends one block, retrying only what is safe to retry.
//
// A permanent error (bad argument, permission, quota) is returned immediately:
// retrying it would just burn the daily 10000-request budget and delay the
// message the user needs to see.
func (f *Fs) uploadPartRetry(ctx context.Context, remote, uploadID string, seq, total int, block []byte) error {
	var lastErr error
	for attempt := 1; attempt <= blockRetries; attempt++ {
		err := f.client.uploadPart(ctx, uploadID, seq, block)
		if err == nil {
			if attempt > 1 {
				fs.Logf(f, "%s: block %d/%d succeeded on attempt %d", remote, seq+1, total, attempt)
			}
			return nil
		}
		lastErr = err

		if classify(err) != classRetryable || attempt == blockRetries {
			break
		}
		wait := blockRetryBaseWait << (attempt - 1)
		fs.Logf(f, "%s: block %d/%d failed (attempt %d/%d), retrying in %v: %v",
			remote, seq+1, total, attempt, blockRetries, wait, err)

		// Sleeping with the context means a stop request takes effect at once
		// instead of after the backoff. The block is re-sent next run anyway:
		// nothing was committed.
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("feishu: upload of %q stopped at block %d/%d: %w",
				remote, seq+1, total, ctx.Err())
		case <-timer.C:
		}
	}

	return fmt.Errorf(
		"feishu: upload of %q failed at block %d/%d after %d attempt(s). "+
			"Feishu keeps an unfinished upload for 24 hours and offers no abort call, "+
			"so it will expire on its own: %w",
		remote, seq+1, total, blockRetries, lastErr)
}
