# GoForConf

GoForConf позволяет искать процедуры и метаданные 1С в подготовленном снимке SQLite и передавать результаты в MCP-клиент.

```text
MCP-клиент  ⇄  stdio / JSON-RPC  ⇄  goforconf-mcp  ⇄  HTTP  ⇄  goforconf-reader  →  SQLite
```

`goforconf-reader` обслуживает HTTP API и открывает существующий снимок SQLite для чтения. `goforconf-mcp` запускается MCP-клиентом и переводит вызовы инструментов в HTTP-запросы к reader. Снимок подготавливает и обновляет отдельный индексатор. Для собственного снимка нужна совместимая версия формата базы. Данные в снимке отражают состояние конфигурации на момент его создания.

## Быстрый запуск в Windows x64

1. Запустите `examples\demo-snapshot\start-reader.bat`. Этот пример включает готовые программы и SQLite-снимок с вымышленными данными. Для запуска достаточно Windows x64.
2. Откройте PowerShell и проверьте HTTP API. Ответы должны содержать `ok`, `HelloDemo` и `Catalog.DemoItems` соответственно.

   ```powershell
   Invoke-RestMethod 'http://127.0.0.1:8088/health'
   (Invoke-RestMethod 'http://127.0.0.1:8088/procedures/search?q=HelloDemo').items
   (Invoke-RestMethod 'http://127.0.0.1:8088/metadata/object?kind=Catalog&name=DemoItems').object
   ```

3. Подключите адаптер по [примеру конфигурации MCP-клиента](examples/mcp-client/README.md). Вызовите `search_code_fts` с `q: "HelloDemo"` или `get_metadata_object` с `kind: "Catalog"` и `name: "DemoItems"`.
4. Для остановки нажмите `Ctrl+C` в окне reader.

Демонстрационный снимок содержит один объект метаданных и одну запись поиска процедуры. Он предназначен для проверки этих операций. Интеграции с живой 1С, Jira и сервисом эмбеддингов в примере выключены.

## Сборка из исходников

Нужны Windows и Go 1.25 или новее. Выполните из корня репозитория:

```powershell
go test ./...
go build -trimpath -o goforconf-reader.exe ./cmd/goforconf-reader
go build -trimpath -o goforconf-mcp.exe ./cmd/goforconf-mcp
```

## Подключение своего снимка

Создайте локальный `config.local.json`. Относительный `databasePath` считается от рабочего каталога reader. `httpAccessFile` должен быть абсолютным локальным путём.

```json
{
  "databasePath": "snapshot.sqlite",
  "httpAddr": "127.0.0.1:8088",
  "httpAccessEnabled": true,
  "httpAccessFile": "C:\\GoForConf\\config\\http_access.clients.json"
}
```

Скопируйте `config/http_access.example.json` в `config/http_access.clients.json`. Добавьте адрес MCP-адаптера. Для запуска обоих компонентов на одном компьютере можно использовать следующую политику:

```json
{
  "version": 1,
  "clients": [{"name": "local-mcp", "ip": "127.0.0.1", "enabled": true}]
}
```

```powershell
.\goforconf-reader.exe --config .\config.local.json serve
```

Проверка IP включена по умолчанию. Демонстрационный запуск использует `httpAccessEnabled: false` вместе с `httpAddr: "127.0.0.1:8088"`. При отключённой проверке доступ определяется адресом прослушивания и сетевыми правилами. HTTP-запросы журналируются. Для явного размещения журналов укажите абсолютный локальный `httpAccessLogDir`.

Для обращения к живой 1С reader использует `oneCDataServiceUrl`, `oneCDataServiceUser` и `oneCDataServicePasswordEnc` в серверном JSON. Настройка доступа к живой 1С включает права сервисной учётной записи и политику полей MCP. Политика применяется адаптером. При выборе сетевого адреса reader учитывайте, что HTTP API доступен клиентам, которым разрешено подключение.

## Шифрование пароля через `encrypt-data-password`

В интерактивной консоли Windows выполните:

```powershell
.\goforconf-reader.exe encrypt-data-password
```

Введите пароль по приглашению. Ввод скрыт. Команда принимает пароль через консоль и выводит в стандартный поток одну строку вида `dpapi:<Base64>`. Файл настроек остаётся без изменений.

Скопируйте строку целиком в `oneCDataServicePasswordEnc` серверного JSON. Удалите поле `oneCDataServicePassword` или оставьте его пустым. Для Jira отдельно зашифруйте её пароль и сохраните результат в `jiraPasswordEnc`.

Windows DPAPI привязывает результат к учётной записи Windows. Выполняйте команду на компьютере reader под той же учётной записью, которая запускает reader. При переносе установки на другой компьютер или смене учётной записи заново зашифруйте пароль на целевой установке. Храните результат в локальной конфигурации с ограниченными правами доступа.
