package config_test

import (
	"os"
	"testing"

	"github.com/taktiks2/go-todo/backend/internal/config"
)

func TestLoad(t *testing.T) {
	// t.Setenv を使うので t.Parallel() は書かない。
	// 併用すると Go が「test using t.Setenv or t.Chdir can not use t.Parallel」で panic する。

	tests := []struct {
		name     string
		port     string
		unsetEnv bool
		wantPort int
		wantErr  bool
	}{
		{name: "PORT が未設定なら既定値 8080", unsetEnv: true, wantPort: 8080},
		{name: "PORT が数値ならその値を使う", port: "9090", wantPort: 9090},
		{name: "PORT が下限 1 でも通る", port: "1", wantPort: 1},
		{name: "PORT が上限 65535 でも通る", port: "65535", wantPort: 65535},
		{name: "PORT が数値でなければエラー", port: "abc", wantErr: true},
		{name: "PORT が空文字ならエラー", port: "", wantErr: true},
		{name: "PORT が 0 ならエラー", port: "0", wantErr: true},
		{name: "PORT が 65536 ならエラー", port: "65536", wantErr: true},
		{name: "PORT が負ならエラー", port: "-1", wantErr: true},

		// strconv.Atoi が何を受けて何を弾くかを明示的に固定する。
		// PORT は docs/DESIGN.md §9 が「最頻出の詰まりどころ」と名指ししており、
		// 「なんとなく通る／通らない」を残さない。
		{name: "PORT の先頭 + は符号として受ける", port: "+8080", wantPort: 8080},
		{name: "PORT の先頭ゼロは 10 進として読む", port: "08080", wantPort: 8080},
		{name: "PORT の前後に空白があればエラー", port: " 8080", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv は元の値を記録してテスト終了時に復元する。
			// 未設定ケースでも一度呼んでおくことで、そのあとの Unsetenv も
			// テスト終了時に巻き戻る。
			t.Setenv("PORT", tt.port)
			if tt.unsetEnv {
				if err := os.Unsetenv("PORT"); err != nil {
					t.Fatalf("PORT を未設定にできない: %v", err)
				}
			}

			got, err := config.Load()

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() = %+v, err = nil; エラーを期待した", got)
				}
				// エラー時に中途半端に埋まった Config を返さないことを固定する。
				// これが無いと `return Config{Port: port}, err` に書き換わっても
				// 全ケース緑のまま素通りする。
				if got != (config.Config{}) {
					t.Errorf("エラー時の Config = %+v, want ゼロ値", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() が予期しないエラーを返した: %v", err)
			}
			if got.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", got.Port, tt.wantPort)
			}
		})
	}
}
