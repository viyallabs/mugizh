param([Parameter(Mandatory=$true)][string]$UpdatesJson)
# Usage: powershell -File tools/tracker/update-backlog.ps1 -UpdatesJson upd.json
# upd.json: { "S-123": { "R": "In Testing", "S": "Approved", "T": "Passing", "X+": "note appended to column X" } }
# Columns: R Status, S Code review, T Tests (values from the Lists sheet), U Owner, V Started, W Done on, X Notes. Back up the xlsx first.
# Updates Backlog rows (sheet5) of the tracker: columns given as inline strings; "X+" appends to Notes.
$path = "C:\Users\manoj\OneDrive\Desktop\mugizh\Resources\healthcare-app-agile-roadmap.xlsx"
$updates = Get-Content $UpdatesJson -Raw -Encoding UTF8 | ConvertFrom-Json
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$z = [System.IO.Compression.ZipFile]::Open($path, [System.IO.Compression.ZipArchiveMode]::Update)
function Read-Part($name) { $e = $z.GetEntry($name); $r = New-Object System.IO.StreamReader($e.Open()); $t = $r.ReadToEnd(); $r.Close(); return $t }
$ss = New-Object System.Xml.XmlDocument; $ss.LoadXml((Read-Part 'xl/sharedStrings.xml'))
$strings = New-Object System.Collections.Generic.List[string]
foreach ($si in $ss.DocumentElement.ChildNodes) { $strings.Add($si.InnerText) }
$sh = New-Object System.Xml.XmlDocument; $sh.PreserveWhitespace = $true; $sh.LoadXml((Read-Part 'xl/worksheets/sheet5.xml'))
$mns = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'
$ns = New-Object System.Xml.XmlNamespaceManager($sh.NameTable); $ns.AddNamespace('m', $mns)
function Cell-Text($c) { if (-not $c) { return '' }; $t = $c.GetAttribute('t'); $v = $c.SelectSingleNode('m:v', $ns); if ($t -eq 's') { return $strings[[int]$v.InnerText] } elseif ($t -eq 'inlineStr') { return $c.InnerText } elseif ($v) { return $v.InnerText } else { return '' } }
function Set-Cell($row, $col, $text) {
  $ref = "$col$($row.GetAttribute('r'))"
  $c = $row.SelectSingleNode("m:c[@r='$ref']", $ns)
  if (-not $c) { throw "cell $ref missing" }
  $style = $c.GetAttribute('s')
  $c.RemoveAll(); $c.SetAttribute('r', $ref); if ($style) { $c.SetAttribute('s', $style) }
  $c.SetAttribute('t', 'inlineStr')
  $is = $sh.CreateElement('is', $mns); $tt = $sh.CreateElement('t', $mns)
  $tt.SetAttribute('xml:space', 'preserve'); $tt.InnerText = $text
  [void]$is.AppendChild($tt); [void]$c.AppendChild($is)
}
$done = @()
foreach ($row in $sh.SelectNodes('//m:sheetData/m:row', $ns)) {
  $b = Cell-Text ($row.SelectSingleNode("m:c[starts-with(@r,'B')]", $ns))
  $u = $updates.PSObject.Properties[$b]
  if (-not $u) { continue }
  foreach ($p in $u.Value.PSObject.Properties) {
    if ($p.Name -eq 'X+') {
      $xc = $row.SelectSingleNode("m:c[@r='X$($row.GetAttribute('r'))']", $ns)
      $old = Cell-Text $xc
      $new = if ($old) { "$old | $($p.Value)" } else { $p.Value }
      Set-Cell $row 'X' $new
    } else { Set-Cell $row $p.Name $p.Value }
  }
  $done += $b
}
$e = $z.GetEntry('xl/worksheets/sheet5.xml'); $e.Delete()
$ne = $z.CreateEntry('xl/worksheets/sheet5.xml')
$s = $ne.Open(); $w = New-Object System.IO.StreamWriter($s, (New-Object System.Text.UTF8Encoding($false))); $sh.Save($w); $w.Close()
$z.Dispose()
"updated: $($done -join ', ')"
