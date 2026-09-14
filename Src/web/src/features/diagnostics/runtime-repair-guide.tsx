export function RuntimeRepairGuide({ detail }: { detail?: string }) {
  return <details className="runtime-repair-guide" open>
    <summary>Runtime 异常的完整修复步骤</summary>
    <p className="runtime-repair-error">当前错误：{detail || '签名、文件完整性或运行时健康检查未通过。'}</p>
    <ol>
      <li>先停止当前 Oktapod 进程，并保留 <code>DataRoot/logs/oktapod-startup.log</code>；不要删除 SQLite、WAL/SHM、项目目录或 Runtime 日志。</li>
      <li>取得与主程序<strong>相同版本、相同操作系统和架构</strong>的完整 Document Runtime 包，以及同一发布包提供的信任公钥文件；不要只拷贝一个 DLL、Python 文件或 LibreOffice 子目录。</li>
      <li>先校验发布包 SHA-256。Windows PowerShell 使用 <code>Get-FileHash .\document-runtime-*.zip -Algorithm SHA256</code>，Linux 使用 <code>sha256sum document-runtime-*.tar.gz</code>，结果必须匹配随包提供的 <code>.sha256</code>。</li>
      <li>将当前损坏或不完整的 <code>runtime/document-runtime</code> 目录改名为带时间的备份，再把完整包解压到原位置；如果 Runtime 位于 <code>DataRoot/runtimes/document-runtime/current</code>，则替换该 <code>current</code> 目录。不要覆盖到其他版本目录。</li>
      <li>启动 Oktapod，等待主服务健康后回到“诊断”或“资源中心”，点击“重新验证/验证运行环境”。只有显示“已就绪/可用”才恢复文档任务。</li>
      <li>仍失败时，依据错误中的具体文件名检查包是否解压完整、权限是否允许当前用户读取，并将启动日志与版本/平台信息一并交给运维；不要通过关闭签名校验或删除数据库来绕过问题。</li>
    </ol>
    <p className="runtime-repair-note">替换的是整个、匹配版本的 Runtime 包；历史项目、数据库和用户文件不需要迁移或删除。</p>
  </details>
}
