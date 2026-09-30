$end=(Get-Date).AddSeconds(90)
$seen=@{}
while((Get-Date) -lt $end){
  $lines = netstat -an -o | Select-String ':42707'
  foreach($l in $lines){
    $t=$l.ToString().Trim()
    if(-not $seen.ContainsKey($t)){
      $seen[$t]=1
      "{0} {1}" -f (Get-Date -Format 'HH:mm:ss.fff'), $t | Out-File -Append <仓库>\tmp_netwatch.log
    }
  }
  Start-Sleep -Milliseconds 250
}
"DONE" | Out-File -Append <仓库>\tmp_netwatch.log
