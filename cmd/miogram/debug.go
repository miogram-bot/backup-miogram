// cmd/miogram/debug.go
package main

import (
    "context"
    "log"
    "time"
    
    "miogram/internal/queue"
)

func debugRedis(ctx context.Context, q *queue.Queue, botID string) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // بررسی وضعیت صف
            length, err := q.Client().LLen(ctx, "inbound:"+botID).Result()
            if err != nil {
                log.Printf("📊 [DEBUG] error getting queue length: %v", err)
            } else {
                log.Printf("📊 [DEBUG] inbound:%s length = %d", botID, length)
            }
            
            // بررسی worker locks
            keys, err := q.Client().Keys(ctx, "miogram:userlock:*").Result()
            if err != nil {
                log.Printf("🔒 [DEBUG] error getting locks: %v", err)
            } else {
                log.Printf("🔒 [DEBUG] active user locks: %d", len(keys))
            }
            
            // بررسی pending updates
            updates, err := q.Client().Keys(ctx, "miogram:update:*").Result()
            if err != nil {
                log.Printf("🔄 [DEBUG] error getting updates: %v", err)
            } else {
                log.Printf("🔄 [DEBUG] processed update ids: %d", len(updates))
            }
        }
    }
}
