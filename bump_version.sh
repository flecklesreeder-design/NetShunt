#!/bin/bash
# 用法: ./bump_version.sh 3.3.4
# 自动同步 app.go / wails.json / setup.iss 中的版本号
# 前端 i18n.js / index.html 已改为动态获取，无需在此脚本中处理

set -e

if [ -z "$1" ]; then
  echo "用法: ./bump_version.sh <版本号>  例如: ./bump_version.sh 3.3.4"
  exit 1
fi

VER="$1"
ROOT="$(cd "$(dirname "$0")" && pwd)"

echo "==> 同步版本号到 $VER"

# app.go: const appVersion = "x.x.x"
sed -i "s|const appVersion = \".*\"|const appVersion = \"$VER\"|" "$ROOT/chenflow-go/app.go"

# wails.json: version / productVersion / fileVersion
sed -i "s|\"version\": \".*\"|\"version\": \"$VER\"|" "$ROOT/chenflow-go/wails.json"
sed -i "s|\"productVersion\": \".*\"|\"productVersion\": \"$VER\"|" "$ROOT/chenflow-go/wails.json"
sed -i "s|\"fileVersion\": \".*\"|\"fileVersion\": \"$VER.0.0\"|" "$ROOT/chenflow-go/wails.json"

# setup.iss: 注释行 / AppVersion / OutputBaseFileName
sed -i "s|^; NetShunt .* Inno Setup|; NetShunt $VER Inno Setup|" "$ROOT/setup.iss"
sed -i "s|^AppVersion=.*|AppVersion=$VER|" "$ROOT/setup.iss"
sed -i "s|^OutputBaseFileName=NetShunt_.*_Setup|OutputBaseFileName=NetShunt_${VER}_Setup|" "$ROOT/setup.iss"

echo "==> 完成。请确认以下文件版本号已更新："
echo "    chenflow-go/app.go        -> const appVersion = \"$VER\""
echo "    chenflow-go/wails.json    -> version / productVersion / fileVersion"
echo "    setup.iss                 -> AppVersion / OutputBaseFileName"
echo "    前端 i18n.js / index.html -> 动态获取，无需手动改"