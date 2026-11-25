#!/bin/bash

# Скрипт сборки MR Scope
# Прекратить выполнение при ошибке
set -e

echo "--- Сборка MR Scope ---"

# Удаление старого бинарника если есть
if [ -f server ]; then
    echo "Удаление старого файла 'server'..."
    rm server
fi

# Проверка и обновление зависимостей
echo "Обновление зависимостей (go mod tidy)..."
go mod tidy

# Сборка проекта
echo "Компиляция..."
go build -o server ./cmd/receiverd/main.go

# Проверка результата
if [ -f server ]; then
    echo "--- Сборка завершена успешно! ---"
    echo "Запуск: ./server"
else
    echo "Ошибка при сборке."
    exit 1
fi
