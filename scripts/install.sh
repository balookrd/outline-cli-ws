#!/bin/bash
set -e

# Цвета для вывода
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}Outline CLI with WebSocket Support - Installer${NC}"
echo "================================================"

# Проверка прав root
if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}Please run as root (sudo)${NC}"
    exit 1
fi

# Определение дистрибутива
if [ -f /etc/debian_version ]; then
    DISTRO="debian"
    PKG_MANAGER="apt-get"
elif [ -f /etc/redhat-release ]; then
    DISTRO="redhat"
    PKG_MANAGER="yum"
elif [ -f /etc/arch-release ]; then
    DISTRO="arch"
    PKG_MANAGER="pacman"
else
    echo -e "${RED}Unsupported distribution${NC}"
    exit 1
fi

echo -e "${YELLOW}Detected distribution: ${DISTRO}${NC}"

# Установка зависимостей
echo -e "${GREEN}Installing dependencies...${NC}"
case $DISTRO in
    debian)
        apt-get update
        apt-get install -y curl git build-essential golang-go jq iptables
        ;;
    redhat)
        yum install -y curl git gcc make golang jq iptables
        ;;
    arch)
        pacman -S --noconfirm curl git base-devel go jq iptables
        ;;
esac

# Создание временной директории
TMP_DIR=$(mktemp -d)
cd $TMP_DIR

# Клонирование репозитория
echo -e "${GREEN}Downloading source code...${NC}"
git clone https://github.com/yourusername/outline-cli-ws.git
cd outline-cli-ws

# Сборка
echo -e "${GREEN}Building...${NC}"
export GOPATH=$TMP_DIR/go
mkdir -p $GOPATH
go mod download
go build -o outline-ws cmd/outline-ws/main.go

# Установка
echo -e "${GREEN}Installing...${NC}"
cp outline-ws /usr/local/bin/
chmod +x /usr/local/bin/outline-ws

# Создание конфигурационной директории
CONFIG_DIR="/etc/outline-ws"
mkdir -p $CONFIG_DIR

# Создание systemd сервиса (опционально)
cat > /etc/systemd/system/outline-ws@.service << EOF
[Unit]
Description=Outline WebSocket VPN for %i
After=network.target

[Service]
Type=simple
User=nobody
Group=nogroup
ExecStart=/usr/local/bin/outline-ws --config /etc/outline-ws connect %i
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Создание скрипта для управления
cat > /usr/local/bin/vpn-ws << 'EOF'
#!/bin/bash
# Wrapper script for outline-ws

CONFIG_DIR="/etc/outline-ws"
mkdir -p $CONFIG_DIR

case "$1" in
    add)
        shift
        /usr/local/bin/outline-ws --config $CONFIG_DIR add "$@"
        ;;
    list)
        /usr/local/bin/outline-ws --config $CONFIG_DIR list
        ;;
    connect)
        if [ -z "$2" ]; then
            echo "Usage: vpn-ws connect <name-or-index>"
            exit 1
        fi
        /usr/local/bin/outline-ws --config $CONFIG_DIR connect "$2"
        ;;
    disconnect)
        /usr/local/bin/outline-ws --config $CONFIG_DIR disconnect
        ;;
    status)
        /usr/local/bin/outline-ws --config $CONFIG_DIR status
        ;;
    remove)
        if [ -z "$2" ]; then
            echo "Usage: vpn-ws remove <name-or-index>"
            exit 1
        fi
        /usr/local/bin/outline-ws --config $CONFIG_DIR remove "$2"
        ;;
    *)
        echo "Outline WebSocket VPN Manager"
        echo "Usage: $0 {add|list|connect|disconnect|status|remove}"
        echo ""
        echo "Commands:"
        echo "  add <key-or-file> [name]  - Add a new server"
        echo "  list                       - List all servers"
        echo "  connect <name-or-index>    - Connect to a server"
        echo "  disconnect                  - Disconnect current server"
        echo "  status                      - Show connection status"
        echo "  remove <name-or-index>      - Remove a server"
        exit 1
        ;;
esac
EOF

chmod +x /usr/local/bin/vpn-ws

# Настройка iptables для перенаправления трафика (опционально)
echo -e "${YELLOW}Do you want to configure iptables for transparent proxy? (y/n)${NC}"
read -r answer
if [ "$answer" = "y" ]; then
    # Создание правил iptables для перенаправления HTTP/HTTPS трафика
    iptables -t nat -N OUTLINE_WS 2>/dev/null || iptables -t nat -F OUTLINE_WS
    iptables -t nat -A OUTPUT -p tcp --dport 80 -j OUTLINE_WS
    iptables -t nat -A OUTPUT -p tcp --dport 443 -j OUTLINE_WS
    iptables -t nat -A OUTLINE_WS -d 127.0.0.0/8 -j RETURN
    iptables -t nat -A OUTLINE_WS -p tcp -j REDIRECT --to-ports 1080

    # Сохранение правил
    if [ -f /etc/debian_version ]; then
        apt-get install -y iptables-persistent
        netfilter-persistent save
    fi

    echo -e "${GREEN}Iptables configured for transparent proxy${NC}"
fi

# Очистка
cd /
rm -rf $TMP_DIR

echo -e "${GREEN}Installation complete!${NC}"
echo ""
echo "Usage:"
echo "  vpn-ws add <key> [name]     - Add a server (supports ss:// and YAML format)"
echo "  vpn-ws list                  - List servers"
echo "  vpn-ws connect <name/index>  - Connect to server"
echo "  vpn-ws disconnect             - Disconnect"
echo "  vpn-ws status                 - Show status"
echo ""
echo "Examples:"
echo "  vpn-ws add 'ss://method:pass@server:port' 'MyServer'"
echo "  vpn-ws add '/path/to/config.yaml' 'WS-Server'"
echo "  vpn-ws connect MyServer"
