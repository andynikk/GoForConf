# Настройка MCP-клиента

1. Запустите `examples\demo-snapshot\start-reader.bat`.
2. Возьмите запись `goforconf-demo` из [mcp-servers.example.json](mcp-servers.example.json) и добавьте её в настройки своего MCP-клиента. Пример использует распространённый формат `mcpServers`. Если клиент настраивается через интерфейс, укажите локальный сервер с транспортом `stdio`.
3. Замените `C:\GoForConf` в `command` и `GOFORCONF_CONFIG` на полный путь к папке комплекта GoForConf. Сохраните двойные обратные косые черты в JSON. Клиент сам запустит `goforconf-mcp.exe`.
4. Проверьте инструмент `get_metadata_object` с аргументами `{"kind":"Catalog","name":"DemoItems"}`. В ответе должно появиться `Catalog.DemoItems`.

`client.json` задаёт HTTP-адрес reader, профиль ответов и политику полей. Путь к политике считается от каталога `client.json`. Для подключения к своему reader измените `httpAddr`. Профиль `economy` по умолчанию сокращает объём ответов. Для полной выдачи задайте `mcpResponseProfile: "full"`.

Пока reader работает, откройте отдельное окно PowerShell в папке комплекта и запустите проверку MCP через стандартный ввод и вывод.

```powershell
$env:GOFORCONF_CONFIG = (Resolve-Path .\examples\mcp-client\client.json).Path
@(
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'
    '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_metadata_object","arguments":{"kind":"Catalog","name":"DemoItems"}}}'
) | .\examples\mcp-client\goforconf-mcp.exe
```

Для запросов к актуальным данным установите `GoForConfData.cfe` в своей базе 1С и настройте reader. [Подключение живых данных 1С](../../docs/live-1c.md)
