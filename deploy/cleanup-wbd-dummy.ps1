<#
  cleanup-wbd-dummy.ps1 — remove the synthetic dummy footprint from the WBD
  FROST-Server, including the duplicate shared entities created by the
  pre-fix upsert race (see docs/testbed-wbd-e2e-findings.md §3.7).

  DRY-RUN BY DEFAULT: prints what it would delete and deletes nothing.
  Pass -Execute to actually delete.

  Requires: curl.exe (bundled with Windows 10+), and $env:FROST_PASSWORD set.

  What it does (in -Execute mode, in this order):
    1. Deletes Things named "Dummy Container*" with properties.synthetic == "true"
       — this cascades their Datastreams and Observations.
    2. Deletes the raced duplicate Sensors / ObservedProperties, but ONLY those
       that are now ORPHANED (zero linked Datastreams). Entities still
       referenced by real WBD datastreams are never touched — this is what makes
       deleting the generically-named "Fill level" ObservedProperty safe.

  Note: keep the dummy data if you still need it for the demo/paper — the
  existing duplicates are harmless (the writer always reuses the lowest id), and
  the code fix prevents new ones. Only run this for a pristine reset.
#>
param([switch]$Execute)

$ErrorActionPreference = 'Stop'
$base = 'https://sta.wbd-rd.nl/FROST-Server/v1.1'
$user = 'write'

if (-not $env:FROST_PASSWORD) { throw 'Set $env:FROST_PASSWORD before running.' }
$cred = "${user}:$($env:FROST_PASSWORD)"

function Get-Json([string]$path) {
    $raw = curl.exe -k -s -u $cred "$base$path"
    if ($LASTEXITCODE -ne 0) { throw "GET $path failed (curl exit $LASTEXITCODE)" }
    return $raw | ConvertFrom-Json
}

function Remove-Entity([string]$path) {
    $code = curl.exe -k -s -o NUL -w '%{http_code}' -X DELETE -u $cred "$base$path"
    return [int]$code
}

$mode = if ($Execute) { 'EXECUTE' } else { 'DRY-RUN' }
Write-Host "== WBD dummy cleanup ($mode) ==" -ForegroundColor Cyan

# --- 1. Synthetic Things (cascades their Datastreams + Observations) ---------
$things = (Get-Json "/Things?`$top=1000").value |
    Where-Object { $_.name -like 'Dummy Container*' -and $_.properties.synthetic -eq 'true' }

Write-Host "`nSynthetic Things: $($things.Count)"
foreach ($t in $things) {
    $id = $t.'@iot.id'
    if ($Execute) {
        $code = Remove-Entity "/Things($id)"
        Write-Host ("  Thing({0}) {1} -> HTTP {2}" -f $id, $t.name, $code)
    }
    else {
        Write-Host ("  [dry-run] would delete Thing({0}) {1}" -f $id, $t.name)
    }
}

# --- 2. Orphaned duplicate shared entities -----------------------------------
# Runs AFTER Things are gone, so the dummy-linked copies now have 0 datastreams.
# In dry-run the reference counts still reflect the pre-delete state (see note).
function Clear-Orphans([string]$entity, [scriptblock]$match) {
    $items = (Get-Json "/${entity}?`$top=1000").value | Where-Object $match
    Write-Host "`n$entity candidates matching name: $($items.Count)"
    foreach ($i in $items) {
        $id = $i.'@iot.id'
        $dsCount = (Get-Json "/${entity}($id)/Datastreams?`$count=true&`$top=1").'@iot.count'
        if ($dsCount -eq 0) {
            if ($Execute) {
                $code = Remove-Entity "/${entity}($id)"
                Write-Host ("  {0}({1}) '{2}' orphaned -> HTTP {3}" -f $entity, $id, $i.name, $code)
            }
            else {
                Write-Host ("  [dry-run] would delete orphaned {0}({1}) '{2}'" -f $entity, $id, $i.name)
            }
        }
        else {
            Write-Host ("  keep {0}({1}) '{2}' — {3} datastream(s) still reference it" -f $entity, $id, $i.name, $dsCount)
        }
    }
}

Clear-Orphans 'Sensors' { $_.name -eq 'Dummy fill-level sensor DUMMY-1 1.0.0' }
Clear-Orphans 'ObservedProperties' { $_.name -eq 'Fill level' }

if (-not $Execute) {
    Write-Host "`n(Dry-run) Note: Sensor/ObservedProperty reference counts above are pre-delete." -ForegroundColor Yellow
    Write-Host "In -Execute mode the dummy Things are deleted first, which orphans the raced" -ForegroundColor Yellow
    Write-Host "copies so they then qualify for deletion. Re-run with -Execute to apply." -ForegroundColor Yellow
}
else {
    Write-Host "`nDone." -ForegroundColor Green
}
