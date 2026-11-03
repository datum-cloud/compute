# Skill: inspect an Instance with a command

Use when the reported status and logs leave a specific question about what is
running inside a customer's Instance. This skill helps the person run a check;
it does not authorize the assistant to run anything.

1. Say what the command will check and why that result would change your
   diagnosis. Do not suggest a command merely because an Instance exists.
2. Pick one named Instance from `compute_instances_list`. A shell is useful only
   for a running general-purpose Instance. If it is absent, starting, stopped,
   or in a runtime without exec, explain that the command cannot run yet.
3. Choose one short, read-only command and its arguments. For a process check,
   `ps -eo pid,ppid,state,args` is a useful starting point; for a file whose
   path the person supplied, `ls -l <path>` can settle whether it exists. Do
   not print environment variables, secrets, tokens, or an entire filesystem
   tree. Avoid commands that change files, send signals, or download code.
4. Call `compute_exec_suggest` with the exact Instance name and command as an
   argument array, such as `["ps", "-eo", "pid,ppid,state,args"]`. Give a
   container name when there are several. The tool checks the target and
   returns a copyable `datumctl compute exec` invocation with safe quoting.
5. Show that exact invocation and say it has **not** been run. Ask the person
   to inspect it and run it themselves, then share only the relevant output.
   Session permission and project quota are checked when they run it. Do not
   invent output or claim you have access to their container.

There is no assistant command-execution tool in this version. A person's
approval to discuss a command is not an approval to execute it, and ordinary
resource plan/apply tools do not run commands inside Instances.
