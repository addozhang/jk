// Demo pipeline showing a pending input with parameters
// (choice + boolean). Used to manually validate the
// "v0.1 sees parameters but can't submit them" gap and to
// drive the v0.2 e2e write-path tests (jk build input -p …).
//
// The Approval stage binds the input result to a local `decision`
// map so the After stage can echo the submitted values; the
// echoed line `decision: ENV=<env> DRY_RUN=<bool>` is what the
// e2e assertion grep-matches to prove the -p values landed.
pipeline {
    agent any
    stages {
        stage('Before') {
            steps {
                echo 'preparing deploy'
            }
        }
        stage('Approval') {
            steps {
                script {
                    def decision = input(
                        message: 'Deploy to which environment?',
                        id: 'deploy',
                        ok: 'Deploy',
                        parameters: [
                            choice(name: 'ENV', choices: ['staging', 'prod'], description: 'Target environment'),
                            booleanParam(name: 'DRY_RUN', defaultValue: true, description: 'Skip side effects')
                        ]
                    )
                    env.DEPLOY_ENV = decision.ENV
                    env.DEPLOY_DRY_RUN = decision.DRY_RUN.toString()
                }
            }
        }
        stage('After') {
            steps {
                echo "decision: ENV=${env.DEPLOY_ENV} DRY_RUN=${env.DEPLOY_DRY_RUN}"
            }
        }
    }
}
