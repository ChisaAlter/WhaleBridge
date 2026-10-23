# Native UI audit account fixture

此插件仅供鲸Bridge原生界面审查使用，供应商认证、模型目录和额度数据全部来自本机回环服务。

## Account management

The fixture provides ten named offline accounts and thirty models. Account labels deliberately include long identifiers so tables, editor sections and status notices can be inspected at narrow widths. No real vendor is contacted and no credentials are read from installed clients.

## Local configuration

`base` must use an HTTP URL on `127.0.0.1`. The loader refuses destinations outside that origin. The fixture is loaded directly from this repository; it does not install packages or execute prepare scripts.

This long reference line exercises preformatted text wrapping: native-ui-audit-offline-readme-reference-with-continuous-identifiers-for-checking-adapter-documentation-on-small-desktop-windows-and-low-height-dialogs-without-clipping-the-page-or-hiding-the-close-button.

## Audit lifecycle

The harness creates a dedicated native home, starts the actual component binary, exchanges its private launch credential for a cookie, and opens the real application page. All application APIs are served by Go. On completion it terminates and joins only its own child processes.
