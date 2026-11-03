# proto.ps1 - RF4 protocol lookup
#
# Usage:
#   .\tools\proto.ps1 21/2              # full protocol profile of one command
#   .\tools\proto.ps1 class hlopebmchhp # one wire class: fields + args code
#   .\tools\proto.ps1 list              # list commands with captured profiles
#
# Sources (all in repo):
#   internal/gameserver/assets/command_responses.json
#   internal/gameserver/assets/command_contract.json
#   internal/gameserver/assets/argcode_table.json
#   internal/wiregen/testdata/wire_spec.json
#   il2csharp_full/.../Assembly-CSharp/*.cs
param(
    [Parameter(Position = 0)][string]$Cmd,
    [Parameter(Position = 1)][string]$Extra
)

$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$assets = Join-Path $root 'internal\gameserver\assets'
$specPath = Join-Path $root 'internal\wiregen\testdata\wire_spec.json'
$dumpPath = Join-Path $root 'il2csharp_full\il2csharp_full\Assembly-CSharp'

function Read-Json($path) {
    if (-not (Test-Path $path)) { throw (('missing file: ' + $path)) }
    return Get-Content $path -Raw -Encoding UTF8 | ConvertFrom-Json
}

$responses = Read-Json (Join-Path $assets 'command_responses.json')
$contract = Read-Json (Join-Path $assets 'command_contract.json')
$argcode = Read-Json (Join-Path $assets 'argcode_table.json')
$spec = Read-Json $specPath

function Get-Prop($obj, $name) {
    if ($null -eq $obj) { return $null }
    $p = $obj.PSObject.Properties | Where-Object { $_.Name -eq $name }
    if ($p) { return $p.Value } else { return $null }
}

function Find-ClientFile($className) {
    if (-not $className) { return $null }
    if (-not (Test-Path $dumpPath)) { return $null }
    $hit = Get-ChildItem $dumpPath -Recurse -File -Filter ($className + '.cs') -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($hit) { return $hit.FullName }
    return $null
}

function Get-Casts($file) {
    $out = @()
    if (-not $file) { return $out }
    foreach ($ln in (Get-Content $file -Encoding UTF8)) {
        foreach ($m in [regex]::Matches($ln, 'lpdfanjcalm<([^>]+)>')) { $out += $m.Groups[1].Value }
    }
    return $out
}

function Resolve-Element($typeName) {
    $name = $typeName -replace '\[\]$', ''
    $hit = $spec.classes.PSObject.Properties | Where-Object { $_.Value.dto -eq $name } | Select-Object -First 1
    if (-not $hit) { return $null }
    $tid = $hit.Value.type_id
    $code = Get-Prop $argcode.by_elem ([string]$tid)
    return [pscustomobject]@{ Wire = $hit.Name; TypeId = $tid; Dto = $name; Code = $code }
}

function Show-Class($className) {
    $cls = Get-Prop $spec.classes $className
    if (-not $cls) { Write-Host ('unknown wire class: ' + $className) -ForegroundColor Red; return }
    $head = 'wire class {0}  type_id={1}  dto={2}' -f $className, $cls.type_id, $cls.dto
    Write-Host $head -ForegroundColor Cyan
    $code = Get-Prop $argcode.by_elem ([string]$cls.type_id)
    if ($code) {
        Write-Host ('  args code when used as array element: {0}  ->  proto.Args(proto.S("{0}"), items...)' -f $code) -ForegroundColor Yellow
    }
    Write-Host '  fields:'
    $fields = @{}
    foreach ($f in $cls.fields) { $fields[[int]$f.idx] = $f }
    for ($i = 0; $i -lt $cls.ops.Count; $i++) {
        $op = $cls.ops[$i]
        $meta = $fields[$i]
        $name = ''
        $zh = ''
        if ($meta) { $name = $meta.name; $zh = $meta.zh }
        Write-Host ('    [{0,2}] {1,-6} {2,-14} {3}' -f $i, $op, $name, $zh)
    }
    $file = Find-ClientFile $className
    if ($file) {
        Write-Host ('  client: ' + $file) -ForegroundColor DarkGray
        $casts = @(Get-Casts $file)
        if ($casts.Count -gt 0) {
            Write-Host ('  client casts: ' + ($casts -join ', ')) -ForegroundColor Yellow
        }
    }
}

function Show-Command($key) {
    $resp = Get-Prop $responses $key
    $con = Get-Prop $contract $key
    if (-not $resp -and -not $con) {
        Write-Host ('unknown command: ' + $key + '  (try: .\tools\proto.ps1 list)') -ForegroundColor Red
        return
    }
    $sem = ''
    if ($resp) { $sem = $resp.sem }
    $names = ''
    if ($con) { $names = ($con.names -join ', ') }
    Write-Host ('command {0}  sem={1}  alias={2}' -f $key, $sem, $names) -ForegroundColor Cyan
    if ($con -and $con.method) { Write-Host ('  client method: ' + $con.method) }
    $wire = ''
    $dto = ''
    $tid = ''
    $isArray = $false
    if ($resp) { $wire = $resp.wire; $dto = $resp.resp; $tid = $resp.tid; $isArray = $resp.array }
    Write-Host ('  response dto: {0}   wire class: {1}   type_id: {2}' -f $dto, $wire, $tid) -ForegroundColor Cyan

    $cls = $null
    if ($wire) { $cls = Get-Prop $spec.classes $wire }

    if ($isArray -and $cls) {
        $cmdCode = Get-Prop $argcode.by_cmd $key
        $elemCode = Get-Prop $argcode.by_elem ([string]$cls.type_id)
        $use = $elemCode
        $src = 'inferred from element class'
        if ($cmdCode) { $use = $cmdCode; $src = 'from capture (by_cmd)' }
        Write-Host ('  response is an array; root args code = {0}  ({1})' -f $use, $src) -ForegroundColor Yellow
    }

    if ($cls) {
        Write-Host '  fields:'
        $fields = @{}
        foreach ($f in $cls.fields) { $fields[[int]$f.idx] = $f }
        $casts = @(Get-Casts (Find-ClientFile $wire))
        $valIdx = 0
        for ($i = 0; $i -lt $cls.ops.Count; $i++) {
            $op = $cls.ops[$i]
            $meta = $fields[$i]
            $name = ''
            $zh = ''
            if ($meta) { $name = $meta.name; $zh = $meta.zh }
            $hint = ''
            if ($op -eq 'VAL') {
                if ($valIdx -lt $casts.Count) {
                    $el = Resolve-Element $casts[$valIdx]
                    if ($el) {
                        $hint = '  => element {0} ({1}/type_id={2}) args code={3}' -f $el.Dto, $el.Wire, $el.TypeId, $el.Code
                    } else {
                        $hint = '  => client cast: ' + $casts[$valIdx]
                    }
                }
                $valIdx++
            }
            Write-Host ('    [{0,2}] {1,-6} {2,-14} {3}{4}' -f $i, $op, $name, $zh, $hint)
        }
        $file = Find-ClientFile $wire
        if ($file) { Write-Host ('  client: ' + $file) -ForegroundColor DarkGray }
    }
}

if (-not $Cmd) {
    Write-Host 'usage: .\tools\proto.ps1 21/2 | .\tools\proto.ps1 class hlopebmchhp | .\tools\proto.ps1 list'
    return
}
if ($Cmd -eq 'class') {
    if (-not $Extra) { Write-Host 'usage: .\tools\proto.ps1 class <wireClass>'; return }
    Show-Class $Extra
    return
}
if ($Cmd -eq 'list') {
    $responses.PSObject.Properties | Sort-Object Name | ForEach-Object {
        '  {0,-8} {1}' -f $_.Name, $_.Value.sem
    }
    return
}
Show-Command $Cmd
