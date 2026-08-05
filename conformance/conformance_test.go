//go:build conformance

package conformance

import (
	"context"
	"testing"
	"time"
)

// TestSluiceComposeConformance 只接受真实 compose 环境；环境不全时显式失败。
func TestSluiceComposeConformance(t *testing.T) {
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("conformance 未配置：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := run(ctx, cfg); err != nil {
		t.Fatalf("conformance 失败：%v", err)
	}
}
