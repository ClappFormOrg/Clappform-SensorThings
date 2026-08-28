<#
  check-wbd-sulo.ps1 — verify what the SULO (REEN) adapter has actually
  registered in a FROST-Server. READ-ONLY: issues only GETs.

  Requires: curl.exe (bundled with Windows 10+), and $env:FROST_PASSWORD set
  for an authenticated target.

  Usage:
    $env:FROST_PASSWORD='the-wbd-password'
    ./check-wbd-sulo.ps1                       # WBD testbed
    ./check-wbd-sulo.ps1 -Local                # local stack on :8081, no auth
    ./check-wbd-sulo.ps1 -Prefix ''            # a target written without a prefix
    ./check-wbd-sulo.ps1 -Detail               # per-datastream newest readings

  Why the odd filters: each entity type gets a different name template from
  internal/oms, so there is no single prefix that matches them all —
  Things are "<P>Sulo Container 514419" but Datastreams are
  "<P>Fill level - Sulo Container 514419" (note the lowercase "level" and the
  em dash). This script encodes the real templates so a zero count means
  "genuinely absent", not "wrong filter".

  Also note: this FROST build rejects OData-v4 contains() with a 400 —
  startswith() is what works here.
#>
param(
    [switch]$Local,
    [string]$Prefix = 'CF_',
    [switch]$Detail,
    [string]$BaseUrl
)

$ErrorActionPreference = 'Stop'

if ($BaseUrl) {
    $base = $BaseUrl.TrimEnd('/')
    $cred = if ($env:FROST_PASSWORD) { "write:$($env:FROST_PASSWORD)" } else { $null }
} elseif ($Local) {
    $base = 'http://localhost:8081/FROST-Server/v1.1'
    $cred = $null
} else {
    $base = 'https://sta.wbd-rd.nl/FROST-Server/v1.1'
    if (-not $env:FROST_PASSWORD) { throw 'Set $env:FROST_PASSWORD before running (or pass -Local).' }
    $cred = "write:$($env:FROST_PASSWORD)"
}

function Invoke-Sta([string]$query) {
    # curl rejects a URL containing raw spaces (exit 3), and OData option
    # values such as "$orderby=phenomenonTime desc" legitimately contain them.
    $query = $query -replace ' ', '%20'
    $args = @('-k', '-s', '--max-time', '40')
    if ($cred) { $args += @('-u', $cred) }
    $raw = curl.exe @args "$base$query"
    if ($LASTEXITCODE -ne 0) {
        throw "GET $query failed (curl exit $LASTEXITCODE). The Clappform TLS-intercepting proxy drops these intermittently — see docs/testbed-wbd-e2e-findings.md 3.1. Retry, or run the query from a container."
    }
    if (-not $raw) { throw "GET $query returned an empty body." }
    if ($raw.TrimStart().StartsWith('<')) {
        throw "GET $query returned HTML, not JSON — a proxy or portal answered instead of FROST. Same root cause as above."
    }
    return $raw | ConvertFrom-Json
}

# URL-encodes a name for use inside an OData startswith() literal.
function Enc([string]$s) { return [uri]::EscapeDataString($s) }

function Get-Count([string]$entity, [string]$namePrefix) {
    $f = "startswith(name,'$namePrefix')"
    $r = Invoke-Sta "/$entity`?`$filter=$(Enc $f)&`$count=true&`$top=0"
    return [int]$r.'@iot.count'
}

Write-Host ""
Write-Host "== SULO footprint in FROST ==" -ForegroundColor Cyan
Write-Host "   target : $base"
Write-Host "   prefix : $(if ($Prefix) { $Prefix } else { '(none)' })"
Write-Host ""

# Name templates per entity, from internal/oms/mapper.go.
$dash = [char]0x2014   # em dash used in DatastreamName
$checks = [ordered]@{
    'Things'             = "${Prefix}Sulo Container "
    'Locations'          = "${Prefix}Location of Sulo Container "
    'FeaturesOfInterest' = "${Prefix}Container location: Sulo Container "
    'Sensors'            = "${Prefix}Sulo fill-level sensor "
    'Datastreams'        = "${Prefix}Fill level $dash Sulo Container "
    'ObservedProperties' = "${Prefix}Fill level"
}

$counts = [ordered]@{}
foreach ($e in $checks.Keys) {
    $n = Get-Count $e $checks[$e]
    $counts[$e] = $n
    $colour = if ($n -gt 0) { 'Green' } else { 'Yellow' }
    Write-Host ("   {0,-19}: {1}" -f $e, $n) -ForegroundColor $colour
}

# Observation totals, summed over the Sulo datastreams.
$dsFilter = "startswith(name,'$($checks['Datastreams'])')"
$ds = Invoke-Sta "/Datastreams`?`$filter=$(Enc $dsFilter)&`$top=200&`$select=name,@iot.id&`$expand=Observations(`$count=true;`$orderby=phenomenonTime desc;`$top=1;`$select=phenomenonTime,result)"

$totalObs = 0
$withData = 0
$newest = $null
foreach ($d in $ds.value) {
    $c = [int]$d.'Observations@iot.count'
    $totalObs += $c
    if ($c -gt 0) {
        $withData++
        $t = $d.Observations[0].phenomenonTime
        if (-not $newest -or $t -gt $newest) { $newest = $t }
    }
}

Write-Host ""
Write-Host ("   observations       : {0} across {1}/{2} datastreams" -f $totalObs, $withData, $ds.value.Count) -ForegroundColor $(if ($totalObs -gt 0) { 'Green' } else { 'Yellow' })
if ($newest) { Write-Host "   newest reading     : $newest" }

if ($Detail) {
    Write-Host ""
    Write-Host "-- per datastream ------------------------------------------" -ForegroundColor Cyan
    foreach ($d in $ds.value | Sort-Object name) {
        $c = [int]$d.'Observations@iot.count'
        $latest = if ($c -gt 0) { "$($d.Observations[0].phenomenonTime)  $($d.Observations[0].result)%" } else { '(no observations)' }
        Write-Host ("   {0,-45} {1,5} obs  {2}" -f $d.name, $c, $latest)
    }
}

Write-Host ""
if ($counts['Things'] -eq 0) {
    Write-Host "   No SULO Things found. Either nothing has been written yet, or the" -ForegroundColor Yellow
    Write-Host "   run used a different ENTITY_NAME_PREFIX (-Prefix to change it)." -ForegroundColor Yellow
} else {
    Write-Host "   Expected steady state: Things = Locations = FeaturesOfInterest =" -ForegroundColor DarkGray
    Write-Host "   Datastreams (one per container slot), Sensors = 1 per distinct device" -ForegroundColor DarkGray
    Write-Host "   model, ObservedProperties = 1. A shortfall against the slot count that" -ForegroundColor DarkGray
    Write-Host "   REEN reports (cmd/sulo-probe) means writes are still failing." -ForegroundColor DarkGray
}
Write-Host ""
