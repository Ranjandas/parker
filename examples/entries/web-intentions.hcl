Kind = "service-intentions"
Name = "web"
Sources = [
  {
    Name   = "api"
    Action = "allow"
  },
  {
    Name   = "*"
    Action = "deny"
  },
]
