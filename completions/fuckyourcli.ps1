# fuckyourcli — PowerShell completion (Register-ArgumentCompleter).
# Installed by setup.ps1 / install.ps1 into <install dir>\completions and
# dot-sourced from $PROFILE (PowerShell loads nothing automatically).
Register-ArgumentCompleter -Native -CommandName fuckyourcli -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $words = @($commandAst.CommandElements | ForEach-Object { $_.ToString() })
    $mainCommands = @('config', 'auto', 'serve', 'sandbox', 'status', 'update', 'models', 'add', 'mcp', 'help')
    $globalFlags  = @('--help', '--version', '--hash')
    $serveFlags   = @('--help', '--addr', '--token', '--state-dir', '--permissions', '--cors', '--max-threads', '--hitl', '--interrupts', '--interaction-timeout')
    $flagValue    = @('--addr', '--token', '--state-dir', '--cors', '--max-threads', '--permissions', '--interaction-timeout', '--ref', '--subdir', '--client-id', '--client-secret-env', '--callback-port', '--scopes')

    # Arguments after command name + completion state: $completed is
    # the number of arguments already closed, $prev the last one.
    $argsAll = @()
    if ($words.Count -ge 2) { $argsAll = @($words[1..($words.Count - 1)]) }
    $completing = -not [string]::IsNullOrEmpty($wordToComplete)
    $completed  = if ($completing) { $argsAll.Count - 1 } else { $argsAll.Count }
    $prev = if ($argsAll.Count -ge 1) {
        if ($completing -and $argsAll.Count -ge 2) { $argsAll[-2] } else { $argsAll[-1] }
    } else { '' }

    # After a flag that requires a value, we don't suggest other options.
    if (-not $completing -and ($flagValue -contains $prev)) { return }

    if ($completed -le 0) {
        $candidates = $mainCommands + $globalFlags
    } elseif ($words[1] -eq 'mcp') {
        if ($completed -eq 1) {
            $candidates = @('status', 'login', 'logout', '--help')
        } elseif ($words[2] -eq 'login' -and $completed -ge 3) {
            $candidates = @('--no-browser', '--client-id', '--client-secret-env', '--callback-port', '--scopes', '--help')
        } else { $candidates = @() }
    } elseif ($words[1] -eq 'add') {
        $candidates = @('--global', '--project', '--ref', '--subdir', '--replace', '--yes', '--help')
    } elseif ($words[1] -eq 'serve') {
        $candidates = $serveFlags
    } elseif ($prev -eq 'config') {
        $candidates = @('show', 'set', 'reset')
    } elseif ($prev -eq 'auto') {
        $candidates = @('enable', 'disable', 'status')
    } elseif ($prev -eq 'sandbox') {
        $candidates = @('setup', 'status', 'reset')
    } elseif ($prev -eq 'models') {
        $candidates = @('update')
    } else {
        $candidates = $globalFlags
    }

    $candidates | Where-Object { $_ -like ($wordToComplete + '*') } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
