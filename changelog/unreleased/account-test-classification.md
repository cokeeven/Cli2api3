### English

- Restrict console quick tests to the selected account and display the request and actual account identifiers.
- Avoid interpreting network port numbers as authentication status codes.
- Label the existing account operation as a status and quota check.
- Add an explicit upstream credential refresh with account-scoped concurrency protection and clear outcomes.
- Track the credential used by each chat attempt so authentication failures after an automatic refresh are recorded safely.

### 中文

- 控制台快速测试严格使用所选账号，并显示请求和实际账号标识。
- 避免将网络端口数字误判为认证状态码。
- 将现有账号操作标明为状态和额度检查。
- 增加明确的上游凭据刷新，并按账号保护并发操作、显示刷新结果。
- 跟踪每次聊天实际使用的凭据，安全记录自动刷新后的认证失败。
